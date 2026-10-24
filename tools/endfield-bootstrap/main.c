/* SPDX-License-Identifier: MIT */
/* Experimental Windows parent-process attribute launcher. Never resumes Games.exe.
 * Build: x86_64-w64-mingw32-gcc -std=c11 -O2 -Wall -Wextra -Werror -municode
 * tools/endfield-bootstrap/main.c -o /tmp/gryph-endfield-bootstrap-atomic-job.exe -lshlwapi
 * See TESTING.md for the isolated-prefix job lifecycle checks.
 */
#define WIN32_LEAN_AND_MEAN
#define _WIN32_WINNT 0x0a00
#include <windows.h>
#include <shlwapi.h>
#include <stdio.h>
#include <wchar.h>

#define COUNT(array) (sizeof(array) / sizeof((array)[0]))

static void report_error(const char *operation, DWORD error)
{
    fprintf(stderr, "FAIL %s error=%lu (0x%08lx)\n", operation,
            (unsigned long)error, (unsigned long)error);
}

static int validate_path(const wchar_t *path, int directory, const char *label)
{
    DWORD attributes;
    /* PathSkipRootW accepts a drive root or UNC server/share, not drive-relative
     * or current-drive-root-relative paths. Its documented limit is MAX_PATH. */
    if (!path || !*path || wcslen(path) >= MAX_PATH || !PathSkipRootW(path)) {
        fprintf(stderr, "FAIL %s requires an absolute Windows path shorter than MAX_PATH\n", label);
        return 1;
    }
    attributes = GetFileAttributesW(path);
    if (attributes == INVALID_FILE_ATTRIBUTES) {
        report_error(label, GetLastError());
        return 1;
    }
    if (!!(attributes & FILE_ATTRIBUTE_DIRECTORY) != directory) {
        fprintf(stderr, "FAIL %s has the wrong file/directory type\n", label);
        return 1;
    }
    return 0;
}

static int close_handle(HANDLE handle, const char *operation)
{
    if (handle && !CloseHandle(handle)) {
        report_error(operation, GetLastError());
        return 1;
    }
    return 0;
}

int wmain(int argc, wchar_t **argv)
{
    STARTUPINFOEXW parent_start = {0};
    STARTUPINFOEXW game_start = {0};
    PROCESS_INFORMATION parent = {0}, game = {0};
    JOBOBJECT_EXTENDED_LIMIT_INFORMATION limits = {0};
    HANDLE parent_job = NULL;
    LPPROC_THREAD_ATTRIBUTE_LIST parent_attributes = NULL;
    LPPROC_THREAD_ATTRIBUTE_LIST attributes = NULL;
    SIZE_T parent_attribute_bytes = 0, attribute_bytes = 0;
    wchar_t parent_command[MAX_PATH + 3], game_command[MAX_PATH + 128];
    DWORD error, wait_result, game_exit_code = 0;
    BOOL size_result;
    int length, parent_initialized = 0, initialized = 0, failed = 1, parent_assigned = 0;
    int job_test = argc == 5 && !wcscmp(argv[1], L"--job-test");
    int argument = job_test ? 2 : 1;

    setvbuf(stdout, NULL, _IONBF, 0);
    if (argc != 4 && !job_test) {
        fputs("Usage: gryph-endfield-bootstrap-r2.exe <absolute Games.exe> <absolute Endfield.exe> <absolute game cwd>\n"
              "   or: gryph-endfield-bootstrap-r2.exe --job-test <absolute Games.exe> <absolute system cmd.exe> <absolute cwd>\n", stderr);
        return 2;
    }
    if (validate_path(argv[argument], 0, "official Games.exe") ||
        validate_path(argv[argument + 1], 0, job_test ? "system cmd.exe" : "Endfield.exe") ||
        validate_path(argv[argument + 2], 1, "child working directory")) return 2;

    length = swprintf(parent_command, COUNT(parent_command), L"\"%ls\"", argv[argument]);
    if (length < 0 || (size_t)length >= COUNT(parent_command)) {
        fputs("FAIL parent command line exceeds its buffer\n", stderr);
        return 2;
    }
    /* The test mode runs the real Windows cmd/ping programs for about 30s,
     * then exits with 7. It does not impersonate a game or exercise the SDK. */
    length = swprintf(game_command, COUNT(game_command), job_test ?
                      L"\"%ls\" /d /c \"ping -n 31 127.0.0.1 >nul && exit /b 7\"" :
                      L"\"%ls\" -launcher_lang=zh-cn -force-vulkan -launcher_sub_channel=1", argv[argument + 1]);
    if (length < 0 || (size_t)length >= COUNT(game_command)) {
        fputs("FAIL game command line exceeds its buffer\n", stderr);
        return 2;
    }

    /* Unnamed, non-inheritable, owned only by this helper. Closing its last
     * handle (including on helper death) kills only its assigned processes. */
    parent_job = CreateJobObjectW(NULL, NULL);
    if (!parent_job) {
        report_error("CreateJobObjectW(suspended parent)", GetLastError());
        goto done;
    }
    limits.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | JOB_OBJECT_LIMIT_BREAKAWAY_OK;
    if (!SetInformationJobObject(parent_job, JobObjectExtendedLimitInformation, &limits, sizeof(limits))) {
        report_error("SetInformationJobObject(KILL_ON_JOB_CLOSE|BREAKAWAY_OK)", GetLastError());
        goto done;
    }
    /* Bind the parent to its kill-on-close job within process creation. Keep
     * this list separate: explicitly assigning the game to this job would
     * override its breakaway request and make helper exit kill the game. */
    size_result = InitializeProcThreadAttributeList(NULL, 1, 0, &parent_attribute_bytes);
    error = GetLastError();
    if (size_result || error != ERROR_INSUFFICIENT_BUFFER || !parent_attribute_bytes) {
        fprintf(stderr, "FAIL InitializeProcThreadAttributeList(parent size) returned=%d bytes=%llu error=%lu\n",
                (int)size_result, (unsigned long long)parent_attribute_bytes, (unsigned long)error);
        goto done;
    }
    parent_attributes = (LPPROC_THREAD_ATTRIBUTE_LIST)LocalAlloc(LMEM_FIXED, parent_attribute_bytes);
    if (!parent_attributes) {
        report_error("LocalAlloc(parent attribute list)", GetLastError());
        goto done;
    }
    if (!InitializeProcThreadAttributeList(parent_attributes, 1, 0, &parent_attribute_bytes)) {
        report_error("InitializeProcThreadAttributeList(parent)", GetLastError());
        goto done;
    }
    parent_initialized = 1;
    if (!UpdateProcThreadAttribute(parent_attributes, 0, PROC_THREAD_ATTRIBUTE_JOB_LIST,
                                   &parent_job, sizeof(parent_job), NULL, NULL)) {
        report_error("UpdateProcThreadAttribute(JOB_LIST)", GetLastError());
        goto done;
    }
    parent_start.StartupInfo.cb = sizeof(parent_start);
    parent_start.StartupInfo.dwFlags = STARTF_USESHOWWINDOW;
    parent_start.StartupInfo.wShowWindow = SW_HIDE;
    parent_start.lpAttributeList = parent_attributes;
    if (!CreateProcessW(argv[argument], parent_command, NULL, NULL, FALSE,
                        EXTENDED_STARTUPINFO_PRESENT | CREATE_SUSPENDED | CREATE_NO_WINDOW,
                        NULL, argv[argument + 2], &parent_start.StartupInfo, &parent)) {
        report_error("CreateProcessW(Games.exe, CREATE_SUSPENDED|JOB_LIST)", GetLastError());
        goto done;
    }
    parent_assigned = 1;
    printf("Bootstrap parent PID=%lu suspended=yes resumed=no job_at_creation=yes kill_on_job_close=yes breakaway_allowed=yes\n",
           (unsigned long)parent.dwProcessId);

    /* The first sizing call is required to fail with ERROR_INSUFFICIENT_BUFFER. */
    size_result = InitializeProcThreadAttributeList(NULL, 1, 0, &attribute_bytes);
    error = GetLastError();
    if (size_result || error != ERROR_INSUFFICIENT_BUFFER || !attribute_bytes) {
        fprintf(stderr, "FAIL InitializeProcThreadAttributeList(size) returned=%d bytes=%llu error=%lu\n",
                (int)size_result, (unsigned long long)attribute_bytes, (unsigned long)error);
        goto done;
    }
    attributes = (LPPROC_THREAD_ATTRIBUTE_LIST)LocalAlloc(LMEM_FIXED, attribute_bytes);
    if (!attributes) {
        report_error("LocalAlloc(attribute list)", GetLastError());
        goto done;
    }
    if (!InitializeProcThreadAttributeList(attributes, 1, 0, &attribute_bytes)) {
        report_error("InitializeProcThreadAttributeList", GetLastError());
        goto done;
    }
    initialized = 1;
    /* CreateProcessW returned a handle with PROCESS_CREATE_PROCESS rights. Keep
     * both the handle and this value alive until the attribute list is deleted. */
    if (!UpdateProcThreadAttribute(attributes, 0, PROC_THREAD_ATTRIBUTE_PARENT_PROCESS,
                                   &parent.hProcess, sizeof(parent.hProcess), NULL, NULL)) {
        report_error("UpdateProcThreadAttribute(PARENT_PROCESS)", GetLastError());
        goto done;
    }
    game_start.StartupInfo.cb = sizeof(game_start);
    game_start.lpAttributeList = attributes;
    if (!CreateProcessW(argv[argument + 1], game_command, NULL, NULL, FALSE,
                        EXTENDED_STARTUPINFO_PRESENT | CREATE_BREAKAWAY_FROM_JOB, NULL,
                        argv[argument + 2], &game_start.StartupInfo, &game)) {
        report_error("CreateProcessW(child, PARENT_PROCESS|BREAKAWAY_FROM_JOB)", GetLastError());
        goto done;
    }
    printf("Child PID=%lu requested_parent_PID=%lu breakaway_requested=yes mode=%s\n",
           (unsigned long)game.dwProcessId, (unsigned long)parent.dwProcessId, job_test ? "job-test" : "game");
    wait_result = WaitForSingleObject(game.hProcess, INFINITE);
    if (wait_result != WAIT_OBJECT_0) {
        if (wait_result == WAIT_FAILED) report_error("WaitForSingleObject(child)", GetLastError());
        else fprintf(stderr, "FAIL WaitForSingleObject(child) result=0x%08lx\n", (unsigned long)wait_result);
        goto done;
    }
    if (!GetExitCodeProcess(game.hProcess, &game_exit_code)) {
        report_error("GetExitCodeProcess(child)", GetLastError());
        goto done;
    }
    printf("Child exited code=%lu (0x%08lx)\n", (unsigned long)game_exit_code, (unsigned long)game_exit_code);
    failed = 0;

done:
    if (initialized) DeleteProcThreadAttributeList(attributes);
    if (attributes && LocalFree(attributes)) {
        report_error("LocalFree(attribute list)", GetLastError());
        failed = 1;
    }
    if (parent_initialized) DeleteProcThreadAttributeList(parent_attributes);
    if (parent_attributes && LocalFree(parent_attributes)) {
        report_error("LocalFree(parent attribute list)", GetLastError());
        failed = 1;
    }
    /* Terminate only the suspended process created above. Never enumerate or
     * terminate the game or any pre-existing process. Always attempt its wait. */
    if (parent.hProcess) {
        BOOL terminated = TerminateProcess(parent.hProcess, 0);
        int job_closed = 0;
        if (!terminated) {
            report_error("TerminateProcess(owned suspended parent)", GetLastError());
            failed = 1;
        }
        if (parent_job) {
            job_closed = !close_handle(parent_job, "CloseHandle(parent job)");
            if (!job_closed) failed = 1;
            parent_job = NULL;
        }
        /* Closing the assigned kill-on-close job is also a termination request
         * for this parent if explicit TerminateProcess itself failed. */
        wait_result = WaitForSingleObject(parent.hProcess,
                                          terminated || (parent_assigned && job_closed) ? INFINITE : 0);
        if (wait_result != WAIT_OBJECT_0) {
            if (wait_result == WAIT_FAILED) report_error("WaitForSingleObject(parent)", GetLastError());
            else fprintf(stderr, "FAIL WaitForSingleObject(parent) result=0x%08lx\n", (unsigned long)wait_result);
            failed = 1;
        } else {
            printf("Bootstrap parent PID=%lu reaped=yes\n", (unsigned long)parent.dwProcessId);
        }
    }
    failed |= close_handle(parent_job, "CloseHandle(parent job before assignment)");
    failed |= close_handle(game.hThread, "CloseHandle(game thread)");
    failed |= close_handle(game.hProcess, "CloseHandle(game process)");
    failed |= close_handle(parent.hThread, "CloseHandle(parent thread)");
    failed |= close_handle(parent.hProcess, "CloseHandle(parent process)");
    /* Preserve the full DWORD game status after successful API and cleanup work. */
    if (failed) return 1;
    ExitProcess(game_exit_code);
}
