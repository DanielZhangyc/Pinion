/* SPDX-License-Identifier: MIT */
/* Real-process controller for main.c's --job-test. Never starts Endfield.
 * Build: x86_64-w64-mingw32-gcc -std=c11 -O2 -Wall -Wextra -Werror -municode
 * tools/endfield-bootstrap/job-lifecycle-test.c
 * -o /tmp/gryph-parent-job-lifecycle-test.exe -lntdll
 * Run only in an isolated prepared prefix; see TESTING.md.
 */
#define WIN32_LEAN_AND_MEAN
#define _WIN32_WINNT 0x0a00
#define WIN32_NO_STATUS
#include <windows.h>
#include <winternl.h>
#undef WIN32_NO_STATUS
#include <ntstatus.h>
#include <tlhelp32.h>
#include <stddef.h>
#include <stdio.h>
#include <stdlib.h>
#include <wchar.h>

#define COUNT(a) (sizeof(a) / sizeof((a)[0]))

/* SystemExtendedHandleInformation, from Wine's include/winternl.h. The public
 * MinGW header omits this structure. ObjectTypeInformation uses its SDK type. */
typedef struct {
    PVOID object;
    ULONG_PTR pid, handle;
    ULONG access;
    USHORT creator, type;
    ULONG attributes, reserved;
} TEST_HANDLE_ENTRY;
typedef struct {
    ULONG_PTR count, reserved;
    TEST_HANDLE_ENTRY entries[1];
} TEST_HANDLE_SNAPSHOT;

_Static_assert(sizeof(void *) == 8, "Build for x64 Windows");
_Static_assert(sizeof(TEST_HANDLE_ENTRY) == 40, "Unexpected native handle layout");
_Static_assert(offsetof(TEST_HANDLE_SNAPSHOT, entries) == 16, "Unexpected native handle header");
_Static_assert(offsetof(PEB, Ldr) == 0x18, "Unexpected native x64 PEB layout");

static int fail(const char *operation)
{
    fprintf(stderr, "FAIL %s win32=%lu\n", operation, (unsigned long)GetLastError());
    return 1;
}

static int fail_nt(const char *operation, NTSTATUS status)
{
    fprintf(stderr, "FAIL %s ntstatus=0x%08lx\n", operation, (unsigned long)status);
    return 1;
}

static int close_handle(HANDLE handle)
{
    if (handle && handle != INVALID_HANDLE_VALUE && !CloseHandle(handle)) return fail("CloseHandle");
    return 0;
}

static int check_job_member(HANDLE process, HANDLE job, BOOL expected, const char *label)
{
    BOOL member;
    if (!IsProcessInJob(process, job, &member)) return fail("IsProcessInJob");
    printf("membership process=%s expected=%u actual=%u\n", label, (unsigned)expected, (unsigned)member);
    if (member != expected) { fputs("FAIL unexpected job membership\n", stderr); return 1; }
    return 0;
}

static int wait_exit(HANDLE process, DWORD timeout, BOOL check_code, DWORD expected, const char *label)
{
    DWORD code, wait = WaitForSingleObject(process, timeout);
    if (wait == WAIT_FAILED) return fail("WaitForSingleObject");
    if (wait != WAIT_OBJECT_0) {
        fprintf(stderr, "FAIL %s did not exit within %lu ms (wait=0x%08lx)\n",
                label, (unsigned long)timeout, (unsigned long)wait);
        return 1;
    }
    if (!GetExitCodeProcess(process, &code)) return fail("GetExitCodeProcess");
    printf("exit process=%s code=%lu\n", label, (unsigned long)code);
    if (check_code && code != expected) { fputs("FAIL unexpected exit code\n", stderr); return 1; }
    return 0;
}

static int find_child(DWORD parent_pid, const wchar_t *name, DWORD *pid)
{
    HANDLE snapshot = CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS, 0);
    PROCESSENTRY32W entry = {.dwSize = sizeof(entry)};
    DWORD error;
    int result = 1;

    *pid = 0;
    if (snapshot == INVALID_HANDLE_VALUE) return fail("CreateToolhelp32Snapshot(processes)");
    if (!Process32FirstW(snapshot, &entry)) { fail("Process32FirstW"); goto done; }
    do {
        if (entry.th32ParentProcessID != parent_pid || _wcsicmp(entry.szExeFile, name)) continue;
        if (*pid) { fputs("FAIL multiple matching test descendants\n", stderr); goto done; }
        *pid = entry.th32ProcessID;
    } while (Process32NextW(snapshot, &entry));
    error = GetLastError();
    if (error != ERROR_NO_MORE_FILES) { SetLastError(error); fail("Process32NextW"); goto done; }
    result = 0;
done:
    return close_handle(snapshot) | result;
}

static int inspect_suspended_parent(HANDLE process, DWORD pid)
{
    HANDLE snapshot = CreateToolhelp32Snapshot(TH32CS_SNAPTHREAD, 0), thread = NULL;
    THREADENTRY32 entry = {.dwSize = sizeof(entry)};
    PROCESS_BASIC_INFORMATION basic;
    PPEB_LDR_DATA ldr;
    ULONG suspended = 0, returned = 0;
    DWORD tid = 0, error;
    SIZE_T bytes = 0;
    NTSTATUS status;
    BOOL wow64;
    int result = 1;

    if (snapshot == INVALID_HANDLE_VALUE) return fail("CreateToolhelp32Snapshot(threads)");
    if (!IsWow64Process(process, &wow64)) { fail("IsWow64Process(parent)"); goto done; }
    if (wow64) { fputs("FAIL parent is not an x64 process\n", stderr); goto done; }
    if (!Thread32First(snapshot, &entry)) { fail("Thread32First"); goto done; }
    do {
        if (entry.th32OwnerProcessID != pid) continue;
        if (tid) { fputs("FAIL suspended parent has multiple Windows threads\n", stderr); goto done; }
        tid = entry.th32ThreadID;
    } while (Thread32Next(snapshot, &entry));
    error = GetLastError();
    if (error != ERROR_NO_MORE_FILES) { SetLastError(error); fail("Thread32Next"); goto done; }
    if (!tid) { fputs("FAIL suspended parent has no Windows thread\n", stderr); goto done; }
    thread = OpenThread(THREAD_QUERY_INFORMATION, FALSE, tid);
    if (!thread) { fail("OpenThread(parent)"); goto done; }
    status = NtQueryInformationThread(thread, (THREADINFOCLASS)35, &suspended, sizeof(suspended), NULL);
    if (status) { fail_nt("NtQueryInformationThread(ThreadSuspendCount)", status); goto done; }
    status = NtQueryInformationProcess(process, ProcessBasicInformation, &basic, sizeof(basic), &returned);
    if (status) { fail_nt("NtQueryInformationProcess(parent)", status); goto done; }
    if (returned != sizeof(basic) || basic.UniqueProcessId != pid || !basic.PebBaseAddress) {
        fputs("FAIL unexpected parent basic information\n", stderr);
        goto done;
    }
    if (!ReadProcessMemory(process, (BYTE *)basic.PebBaseAddress + offsetof(PEB, Ldr),
                           &ldr, sizeof(ldr), &bytes)) {
        fail("ReadProcessMemory(parent PEB.Ldr)");
        goto done;
    }
    printf("parent pid=%lu initial_tid=%lu threads=1 suspend_count=%lu peb_ldr=%p\n",
           (unsigned long)pid, (unsigned long)tid, (unsigned long)suspended, (void *)ldr);
    if (bytes != sizeof(ldr) || suspended != 1 || ldr) {
        fputs("FAIL parent is not suspended before the PE loader\n", stderr);
        goto done;
    }
    result = 0;
done:
    result |= close_handle(thread);
    return close_handle(snapshot) | result;
}

static TEST_HANDLE_SNAPSHOT *get_handles(void)
{
    ULONG size = 65536, returned;
    NTSTATUS status;
    TEST_HANDLE_SNAPSHOT *snapshot;

    for (;;) {
        snapshot = malloc(size);
        if (!snapshot) { fputs("FAIL allocating native handle snapshot\n", stderr); return NULL; }
        returned = 0;
        status = NtQuerySystemInformation((SYSTEM_INFORMATION_CLASS)64, snapshot, size, &returned);
        if (status == STATUS_INFO_LENGTH_MISMATCH) {
            free(snapshot);
            if (size >= 16 * 1024 * 1024 || returned > 16 * 1024 * 1024) {
                fputs("FAIL native handle snapshot exceeds 16 MiB\n", stderr);
                return NULL;
            }
            size = returned > size ? returned : size * 2;
            if (size > 16 * 1024 * 1024) size = 16 * 1024 * 1024;
            continue;
        }
        if (status) { fail_nt("NtQuerySystemInformation(SystemExtendedHandleInformation)", status); free(snapshot); return NULL; }
        if (returned < offsetof(TEST_HANDLE_SNAPSHOT, entries) || returned > size ||
            snapshot->count > (returned - offsetof(TEST_HANDLE_SNAPSHOT, entries)) / sizeof(TEST_HANDLE_ENTRY)) {
            fputs("FAIL inconsistent native handle snapshot size\n", stderr);
            free(snapshot);
            return NULL;
        }
        return snapshot;
    }
}

static int inspect_helper_job(HANDLE helper, DWORD helper_pid, HANDLE parent, DWORD parent_pid, HANDLE child)
{
    TEST_HANDLE_SNAPSHOT *snapshot = get_handles();
    JOBOBJECT_BASIC_PROCESS_ID_LIST members;
    HANDLE duplicate = NULL;
    ULONG_PTR i;
    int found = 0, result = 1;

    if (!snapshot) return 1;
    for (i = 0; i < snapshot->count; ++i) {
        union { OBJECT_TYPE_INFORMATION align; BYTE data[1024]; } buffer;
        OBJECT_TYPE_INFORMATION *type = (void *)buffer.data;
        NTSTATUS status;

        if (snapshot->entries[i].pid != helper_pid) continue;
        if (!DuplicateHandle(helper, (HANDLE)snapshot->entries[i].handle, GetCurrentProcess(),
                             &duplicate, 0, FALSE, DUPLICATE_SAME_ACCESS)) {
            fail("DuplicateHandle(test helper)");
            goto done;
        }
        status = NtQueryObject(duplicate, ObjectTypeInformation, buffer.data, sizeof(buffer), NULL);
        if (status) { fail_nt("NtQueryObject(ObjectTypeInformation)", status); goto done; }
        if (type->TypeName.Length == 3 * sizeof(wchar_t) && !wmemcmp(type->TypeName.Buffer, L"Job", 3)) {
            if (found++) { fputs("FAIL helper has multiple job handles\n", stderr); goto done; }
            if (check_job_member(parent, duplicate, TRUE, "parent-in-helper-job") ||
                (child && check_job_member(child, duplicate, FALSE, "child-in-helper-job"))) goto done;
            /* This Wine's NtQueryInformationJobObject(ExtendedLimitInformation)
             * returns a zero-filled stub, although setting limits is implemented.
             * Verify breakaway by membership and kill-on-close by helper death;
             * a successful flag query would not establish either behavior. */
            if (!QueryInformationJobObject(duplicate, JobObjectBasicProcessIdList, &members, sizeof(members), NULL)) {
                fail("QueryInformationJobObject(process IDs)");
                goto done;
            }
            if (members.NumberOfAssignedProcesses != 1 || members.NumberOfProcessIdsInList != 1 ||
                members.ProcessIdList[0] != parent_pid) {
                fputs("FAIL helper job contains unexpected processes\n", stderr);
                goto done;
            }
            printf("helper_job source_handle=%p assigned_processes=1 parent_pid=%lu "
                   "limit_flags_readback=unsupported_verify_behavior\n",
                   (HANDLE)snapshot->entries[i].handle, (unsigned long)parent_pid);
        }
        if (close_handle(duplicate)) { duplicate = NULL; goto done; }
        duplicate = NULL;
    }
    if (!found) { fputs("FAIL helper job handle absent\n", stderr); goto done; }
    puts("helper_job duplicate_handles_closed=yes");
    result = 0;
done:
    result |= close_handle(duplicate);
    free(snapshot);
    return result;
}

/* Failure cleanup applies only to handles of this controller's test descendants,
 * after a failed assertion has already made the test fail. It is never evidence
 * for the kill-on-close assertion. */
static int cleanup_process(HANDLE process, const char *label)
{
    DWORD wait;
    if (!process) return 0;
    wait = WaitForSingleObject(process, 0);
    if (wait == WAIT_OBJECT_0) return 0;
    if (wait == WAIT_FAILED) return fail("WaitForSingleObject(failure cleanup)");
    if (wait != WAIT_TIMEOUT) { fputs("FAIL unexpected cleanup wait result\n", stderr); return 1; }
    fprintf(stderr, "CLEANUP terminating owned test process=%s\n", label);
    if (!TerminateProcess(process, 1)) return fail("TerminateProcess(failure cleanup)");
    return wait_exit(process, 5000, FALSE, 0, label);
}

static int close_debug_handles(DEBUG_EVENT *event)
{
    int result = 0;
    switch (event->dwDebugEventCode) {
    case CREATE_PROCESS_DEBUG_EVENT:
        result |= close_handle(event->u.CreateProcessInfo.hFile);
        result |= close_handle(event->u.CreateProcessInfo.hThread);
        result |= close_handle(event->u.CreateProcessInfo.hProcess);
        event->u.CreateProcessInfo.hFile = NULL;
        event->u.CreateProcessInfo.hThread = NULL;
        event->u.CreateProcessInfo.hProcess = NULL;
        break;
    case CREATE_THREAD_DEBUG_EVENT:
        result |= close_handle(event->u.CreateThread.hThread);
        event->u.CreateThread.hThread = NULL;
        break;
    case LOAD_DLL_DEBUG_EVENT:
        result |= close_handle(event->u.LoadDll.hFile);
        event->u.LoadDll.hFile = NULL;
        break;
    }
    return result;
}

static int inspect_debug_identity(DWORD pid, DWORD expected_parent, BOOL *console)
{
    HANDLE snapshot = CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS, 0);
    PROCESSENTRY32W entry = {.dwSize = sizeof(entry)};
    int result = 1;
    if (snapshot == INVALID_HANDLE_VALUE) return fail("CreateToolhelp32Snapshot(debug identity)");
    if (!Process32FirstW(snapshot, &entry)) { fail("Process32FirstW(debug identity)"); goto done; }
    do {
        if (entry.th32ProcessID != pid) continue;
        printf("debug_identity pid=%lu ppid=%lu name=%ls\n", (unsigned long)pid,
               (unsigned long)entry.th32ParentProcessID, entry.szExeFile);
        *console = !_wcsicmp(entry.szExeFile, L"conhost.exe");
        if (entry.th32ParentProcessID != expected_parent ||
            (!*console && _wcsicmp(entry.szExeFile, L"Games.exe"))) {
            fputs("FAIL debug creation event is not the helper's Games.exe or console host\n", stderr);
            goto done;
        }
        result = 0;
        goto done;
    } while (Process32NextW(snapshot, &entry));
    if (GetLastError() != ERROR_NO_MORE_FILES) fail("Process32NextW(debug identity)");
    else fputs("FAIL debug creation process absent from snapshot\n", stderr);
done:
    return close_handle(snapshot) | result;
}

/* Continue every exit event before returning. The debugger's exit policy is
 * disabled, so debugger teardown cannot supply the parent-termination proof. */
static int drain_debug_exits(DWORD helper_pid, DWORD parent_pid, DWORD console_pid)
{
    ULONGLONG deadline = GetTickCount64() + 10000;
    BOOL helper_exited = FALSE, parent_exited = !parent_pid, console_exited = !console_pid;
    int result = 0;

    while (!helper_exited || !parent_exited || !console_exited) {
        DEBUG_EVENT event = {0};
        DWORD continuation = DBG_CONTINUE;
        if (GetTickCount64() >= deadline) {
            fputs("FAIL draining debug exit events exceeded 10 seconds\n", stderr);
            return 1;
        }
        if (!WaitForDebugEvent(&event, 100)) {
            if (GetLastError() == ERROR_SEM_TIMEOUT) continue;
            return fail("WaitForDebugEvent(exit drain)");
        }
        printf("debug_drain event=%lu pid=%lu tid=%lu\n", (unsigned long)event.dwDebugEventCode,
               (unsigned long)event.dwProcessId, (unsigned long)event.dwThreadId);
        if (event.dwDebugEventCode == EXIT_PROCESS_DEBUG_EVENT) {
            if (event.dwProcessId == helper_pid) helper_exited = TRUE;
            else if (event.dwProcessId == parent_pid) parent_exited = TRUE;
            else if (event.dwProcessId == console_pid) console_exited = TRUE;
            else { fputs("FAIL unexpected debuggee exit\n", stderr); result = 1; }
        } else if (event.dwDebugEventCode == CREATE_PROCESS_DEBUG_EVENT) {
            fputs("FAIL unexpected process created during debug exit drain\n", stderr);
            result = 1;
            result |= cleanup_process(event.u.CreateProcessInfo.hProcess, "unexpected-test-debuggee");
        } else if (event.dwDebugEventCode == EXCEPTION_DEBUG_EVENT) {
            fputs("FAIL unexpected exception during debug exit drain\n", stderr);
            result = 1;
            continuation = DBG_EXCEPTION_NOT_HANDLED;
        }
        result |= close_debug_handles(&event);
        if (!ContinueDebugEvent(event.dwProcessId, event.dwThreadId, continuation))
            return fail("ContinueDebugEvent(exit drain)");
    }
    printf("debug_exit_events helper=yes parent=%s console=%s continued=yes\n",
           parent_pid ? "yes" : "not-created", console_pid ? "yes" : "not-created");
    return result;
}

static int parent_create_death(PROCESS_INFORMATION *helper, HANDLE *parent, DWORD *parent_pid)
{
    DEBUG_EVENT event = {0};
    CONTEXT context = {.ContextFlags = CONTEXT_CONTROL};
    ULONGLONG deadline = GetTickCount64() + 10000;
    DWORD discovered = 0, previous;
    DWORD console_pid = 0;
    HANDLE console_process = NULL;
    BOOL pending = FALSE, breakpoint_seen = FALSE, console_breakpoint_seen = FALSE;
    int result = 1;

    if (!DebugSetProcessKillOnExit(FALSE)) { fail("DebugSetProcessKillOnExit(FALSE)"); goto done; }
    puts("debugger kill_on_exit=no");
    for (;;) {
        if (GetTickCount64() >= deadline) { fputs("FAIL parent creation debug event exceeded 10 seconds\n", stderr); goto done; }
        if (!WaitForDebugEvent(&event, 100)) {
            if (GetLastError() == ERROR_SEM_TIMEOUT) continue;
            fail("WaitForDebugEvent(parent startup)");
            goto done;
        }
        pending = TRUE;
        printf("debug_startup event=%lu pid=%lu tid=%lu\n", (unsigned long)event.dwDebugEventCode,
               (unsigned long)event.dwProcessId, (unsigned long)event.dwThreadId);
        if (event.dwDebugEventCode == CREATE_PROCESS_DEBUG_EVENT && event.dwProcessId != helper->dwProcessId) {
            BOOL is_console = FALSE;
            if (inspect_debug_identity(event.dwProcessId, helper->dwProcessId, &is_console)) {
                cleanup_process(event.u.CreateProcessInfo.hProcess, "unexpected-test-debuggee"); goto done;
            }
            if (is_console) {
                if (console_pid) { fputs("FAIL multiple helper console hosts\n", stderr); goto done; }
                console_pid = event.dwProcessId;
                console_process = OpenProcess(PROCESS_QUERY_INFORMATION | PROCESS_TERMINATE | SYNCHRONIZE,
                                              FALSE, console_pid);
                if (!console_process) { fail("OpenProcess(test console host)"); goto done; }
            } else {
                *parent_pid = event.dwProcessId;
                *parent = OpenProcess(PROCESS_QUERY_INFORMATION | PROCESS_VM_READ | PROCESS_TERMINATE | SYNCHRONIZE,
                                      FALSE, *parent_pid);
                if (!*parent) { fail("OpenProcess(parent debug event)"); goto done; }
                break;
            }
        }
        if ((event.dwProcessId != helper->dwProcessId && event.dwProcessId != console_pid) ||
            event.dwDebugEventCode == EXIT_PROCESS_DEBUG_EVENT) {
            fputs("FAIL helper exited or unexpected debuggee before parent event\n", stderr);
            goto done;
        }
        if (event.dwDebugEventCode == EXCEPTION_DEBUG_EVENT) {
            BOOL *seen = event.dwProcessId == helper->dwProcessId ? &breakpoint_seen : &console_breakpoint_seen;
            if (!event.u.Exception.dwFirstChance || *seen ||
                event.u.Exception.ExceptionRecord.ExceptionCode != (DWORD)EXCEPTION_BREAKPOINT) {
                fputs("FAIL unexpected helper startup exception\n", stderr);
                goto done;
            }
            *seen = TRUE;
        }
        if (close_debug_handles(&event)) goto done;
        if (!ContinueDebugEvent(event.dwProcessId, event.dwThreadId, DBG_CONTINUE)) {
            fail("ContinueDebugEvent(helper startup)"); goto done;
        }
        pending = FALSE;
    }
    /* A creation event suspends the parent, not its caller. Synchronize the
     * helper's actual stopped context before asserting that no cmd exists. */
    previous = SuspendThread(helper->hThread);
    if (previous == (DWORD)-1) { fail("SuspendThread(test helper)"); goto done; }
    if (previous) { fputs("FAIL helper initial thread was already suspended\n", stderr); goto done; }
    if (!GetThreadContext(helper->hThread, &context)) { fail("GetThreadContext(stopped helper)"); goto done; }
    if (find_child(*parent_pid, L"cmd.exe", &discovered)) goto done;
    if (discovered) { fputs("FAIL cmd already observable at parent initial loading event\n", stderr); goto done; }
    printf("boundary=parent-initial-loading caller_CreateProcess_returned=not_determined "
           "helper_rip=0x%llx parent_pid=%lu cmd_observed=no\n",
           (unsigned long long)context.Rip, (unsigned long)*parent_pid);
    if (check_job_member(helper->hProcess, NULL, FALSE, "helper-any-job") ||
        inspect_suspended_parent(*parent, *parent_pid) ||
        inspect_helper_job(helper->hProcess, helper->dwProcessId, *parent, *parent_pid, NULL)) goto done;
    if (close_debug_handles(&event)) goto done;
    if (!TerminateProcess(helper->hProcess, 99)) { fail("TerminateProcess(helper at parent creation event)"); goto done; }
    if (!ContinueDebugEvent(event.dwProcessId, event.dwThreadId, DBG_CONTINUE)) {
        fail("ContinueDebugEvent(parent creation)"); goto done;
    }
    pending = FALSE;
    if (drain_debug_exits(helper->dwProcessId, *parent_pid, console_pid) ||
        wait_exit(helper->hProcess, 5000, TRUE, 99, "helper-at-parent-initial-loading") ||
        wait_exit(*parent, 5000, FALSE, 0, "parent-at-initial-loading") ||
        (console_process && wait_exit(console_process, 5000, FALSE, 0, "helper-console-host"))) {
        cleanup_process(console_process, "console-after-debug-verification-failure");
        close_handle(console_process);
        return 1;
    }
    if (find_child(*parent_pid, L"cmd.exe", &discovered)) { close_handle(console_process); return 1; }
    if (discovered) { fputs("FAIL cmd remains after parent creation test\n", stderr); close_handle(console_process); return 1; }
    if (close_handle(console_process)) return 1;
    puts("parent_initial_loading no_cmd_create_event=yes parent_reaped_before_debugger_exit=yes");
    return 0;
done:
    /* All handles identify descendants of this controller. Cleanup is logged
     * and cannot turn an assertion failure into a successful job check. */
    result |= cleanup_process(helper->hProcess, "helper-debug-failure");
    result |= cleanup_process(*parent, "parent-debug-failure");
    result |= cleanup_process(console_process, "console-debug-failure");
    if (pending) {
        result |= close_debug_handles(&event);
        if (!ContinueDebugEvent(event.dwProcessId, event.dwThreadId,
                                event.dwDebugEventCode == EXCEPTION_DEBUG_EVENT ? DBG_EXCEPTION_NOT_HANDLED : DBG_CONTINUE))
            result |= fail("ContinueDebugEvent(failure cleanup)");
    }
    result |= drain_debug_exits(helper->dwProcessId, *parent_pid, console_pid);
    result |= close_handle(console_process);
    return result;
}

int wmain(int argc, wchar_t **argv)
{
    STARTUPINFOW startup = {.cb = sizeof(startup)};
    PROCESS_INFORMATION helper = {0};
    SECURITY_ATTRIBUTES inherit = {sizeof(inherit), NULL, TRUE};
    HANDLE log = INVALID_HANDLE_VALUE, input = INVALID_HANDLE_VALUE, parent = NULL, child = NULL;
    wchar_t system[MAX_PATH], command[4 * MAX_PATH + 80];
    DWORD parent_pid = 0, child_pid = 0, size, wait;
    ULONGLONG deadline;
    int length, death, creation, result = 1;

    if (setvbuf(stdout, NULL, _IONBF, 0)) { fputs("FAIL unbuffered stdout\n", stderr); return 1; }
    if (argc != 5 || (wcscmp(argv[1], L"normal") && wcscmp(argv[1], L"helper-death") &&
                      wcscmp(argv[1], L"parent-create-death"))) {
        fputs("Usage: job-lifecycle-test.exe normal|helper-death|parent-create-death <candidate helper> <real Games.exe> <new helper log>\n", stderr);
        return 2;
    }
    death = !wcscmp(argv[1], L"helper-death");
    creation = !wcscmp(argv[1], L"parent-create-death");
    if (wcschr(argv[2], L'"') || wcschr(argv[3], L'"')) {
        fputs("FAIL executable paths cannot contain quotes\n", stderr);
        return 2;
    }
    if (check_job_member(GetCurrentProcess(), NULL, FALSE, "controller-any-job")) return 1;
    size = GetSystemDirectoryW(system, COUNT(system));
    if (!size) return fail("GetSystemDirectoryW");
    if (size >= COUNT(system)) { fputs("FAIL system directory too long\n", stderr); return 1; }
    length = swprintf(command, COUNT(command), L"\"%ls\" --job-test \"%ls\" \"%ls\\cmd.exe\" \"%ls\"",
                      argv[2], argv[3], system, system);
    if (length < 0 || (size_t)length >= COUNT(command)) { fputs("FAIL helper command too long\n", stderr); return 2; }
    log = CreateFileW(argv[4], GENERIC_WRITE, FILE_SHARE_READ, &inherit, CREATE_NEW, FILE_ATTRIBUTE_NORMAL, NULL);
    if (log == INVALID_HANDLE_VALUE) return fail("CreateFileW(new helper log)");
    input = CreateFileW(L"NUL", GENERIC_READ, FILE_SHARE_READ | FILE_SHARE_WRITE, &inherit,
                        OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, NULL);
    if (input == INVALID_HANDLE_VALUE) { fail("CreateFileW(NUL)"); goto done; }
    startup.dwFlags = STARTF_USESTDHANDLES;
    startup.hStdInput = input;
    startup.hStdOutput = startup.hStdError = log;
    if (!CreateProcessW(argv[2], command, NULL, NULL, TRUE, CREATE_NO_WINDOW | (creation ? DEBUG_PROCESS : 0),
                        NULL, system, &startup, &helper)) {
        fail("CreateProcessW(test helper)");
        goto done;
    }
    printf("test_helper pid=%lu mode=%ls\n", (unsigned long)helper.dwProcessId, argv[1]);
    if (close_handle(input)) { input = INVALID_HANDLE_VALUE; goto done; }
    input = INVALID_HANDLE_VALUE;
    if (close_handle(log)) { log = INVALID_HANDLE_VALUE; goto done; }
    log = INVALID_HANDLE_VALUE;
    if (creation) {
        result = parent_create_death(&helper, &parent, &parent_pid);
        goto done;
    }
    deadline = GetTickCount64() + 10000;
    while (!child) {
        wait = WaitForSingleObject(helper.hProcess, 0);
        if (wait == WAIT_FAILED) { fail("WaitForSingleObject(helper startup)"); goto done; }
        if (wait != WAIT_TIMEOUT) { fputs("FAIL helper exited before its test child was observed\n", stderr); goto done; }
        if (!parent_pid && find_child(helper.dwProcessId, L"Games.exe", &parent_pid)) goto done;
        if (parent_pid && !parent) {
            parent = OpenProcess(PROCESS_QUERY_INFORMATION | PROCESS_VM_READ | PROCESS_TERMINATE | SYNCHRONIZE,
                                  FALSE, parent_pid);
            if (!parent) { fail("OpenProcess(test parent)"); goto done; }
        }
        if (parent_pid && find_child(parent_pid, L"cmd.exe", &child_pid)) goto done;
        if (child_pid) {
            child = OpenProcess(PROCESS_QUERY_INFORMATION | PROCESS_TERMINATE | SYNCHRONIZE, FALSE, child_pid);
            if (!child) { fail("OpenProcess(test child)"); goto done; }
            break;
        }
        if (GetTickCount64() >= deadline) { fputs("FAIL helper startup exceeded 10 seconds\n", stderr); goto done; }
        Sleep(100);
    }
    printf("test_descendants parent_pid=%lu child_pid=%lu\n", (unsigned long)parent_pid, (unsigned long)child_pid);
    if (check_job_member(helper.hProcess, NULL, FALSE, "helper-any-job") ||
        check_job_member(child, NULL, FALSE, "child-any-job") ||
        inspect_suspended_parent(parent, parent_pid) ||
        inspect_helper_job(helper.hProcess, helper.dwProcessId, parent, parent_pid, child)) goto done;
    if (death) {
        if (!TerminateProcess(helper.hProcess, 99)) { fail("TerminateProcess(test helper)"); goto done; }
        if (wait_exit(helper.hProcess, 5000, TRUE, 99, "helper") ||
            wait_exit(parent, 5000, FALSE, 0, "parent-after-helper-death")) goto done;
        wait = WaitForSingleObject(child, 0);
        if (wait == WAIT_FAILED) { fail("WaitForSingleObject(child survival)"); goto done; }
        if (wait != WAIT_TIMEOUT) { fputs("FAIL breakaway child did not survive helper death\n", stderr); goto done; }
        puts("breakaway_child alive_after_helper_death=yes");
    }
    if (wait_exit(child, 40000, TRUE, 7, "real-cmd-child")) goto done;
    if (!death && (wait_exit(helper.hProcess, 5000, TRUE, 7, "helper") ||
                   wait_exit(parent, 5000, FALSE, 0, "parent-after-normal-exit"))) goto done;
    result = 0;
done:
    if (result) {
        result |= cleanup_process(helper.hProcess, "helper");
        result |= cleanup_process(parent, "parent");
        result |= cleanup_process(child, "child");
    }
    result |= close_handle(child);
    result |= close_handle(parent);
    result |= close_handle(helper.hThread);
    result |= close_handle(helper.hProcess);
    result |= close_handle(input);
    result |= close_handle(log);
    if (!result && creation)
        puts("PASS parent_initial_loading_job parent_suspended=yes parent_job_verified=yes "
             "parent_reaped=yes cmd_observed=no caller_CreateProcess_returned=not_determined");
    else if (!result)
        puts("PASS real_job_lifecycle parent_suspended=yes parent_job_verified=yes child_breakaway=yes child_exit=7");
    if (fflush(stdout) == EOF || ferror(stdout)) { fputs("FAIL writing controller output\n", stderr); result = 1; }
    return result;
}
