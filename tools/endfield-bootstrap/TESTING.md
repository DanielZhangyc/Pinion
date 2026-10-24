# Endfield bootstrap checks

Compile only the helper; this does not update the embedded executable or the app:

```sh
x86_64-w64-mingw32-gcc -std=c11 -O2 -Wall -Wextra -Werror -municode \
  tools/endfield-bootstrap/main.c \
  -o /tmp/gryph-endfield-bootstrap-atomic-job.exe -lshlwapi
```

The helper targets the Windows 10 process-attribute API used by `JOB_LIST`; the
configured Wine prefix runs as Windows 11. Unsupported attributes fail the launch.

Use a separate test prefix. Set
`RUNTIME` to the matching runtime, `TEST_PREFIX` to
that test prefix, and `OFFICIAL_GAMES` to the absolute Windows path of the real
official `Games.exe` visible in that prefix. The helper leaves `Games.exe` suspended
and runs the prefix's real `cmd.exe` and `ping.exe`; it does not start Endfield.

```sh
WINEPREFIX="$TEST_PREFIX" "$RUNTIME/bin/wine" \
  /tmp/gryph-endfield-bootstrap-atomic-job.exe --job-test \
  "$OFFICIAL_GAMES" 'C:\windows\system32\cmd.exe' 'C:\windows\system32' \
  > /tmp/gryph-parent-atomic-job-normal.log 2>&1
```

Expected: the log reports `job_at_creation=yes`; the real child exits with code 7
after about 30 seconds; the helper reports the suspended parent reaped and exits
with code 7. A successful compile or this log marker alone does not verify job
membership or forced-exit behavior.

Before deployment, perform these additional real-process checks in the test
prefix with a controller or debugger retaining query handles to the created
processes. Do not change the production game session or its job handles.

- Query `IsProcessInJob`: the suspended parent belongs to the helper's job and
  the breakaway child does not. Query the parent's initial thread suspension count
  and `PEB.Ldr`: it remains suspended and has not entered the PE loader.
- Stop the test helper immediately after successful parent creation, before game
  child creation, then forcibly terminate that helper. Confirm the suspended
  parent exits without an explicit parent termination call from the controller.
- Repeat after child creation. The parent must exit on helper death, while the
  real breakaway child continues and exits with code 7. Retain the child process
  handle to obtain its exit status after helper death.
- Exercise helper termination during process creation and check for surviving
  parents. Report the number of completed iterations and any remaining processes.
- Give a real existing non-executable regular file as the parent executable to
  make `CreateProcessW` fail after the job attribute list is prepared. Expect
  failure status 1 and no created parent. Use a separate run with such a file as
  the child path to exercise cleanup after successful parent creation.

The parent and child use independent attribute lists. Never copy the parent's
`JOB_LIST` attribute into the child's `PARENT_PROCESS` list: explicit job lists
still apply when `CREATE_BREAKAWAY_FROM_JOB` is requested.
