# HPatch core

Source: [sisong/HDiffPatch v5.1.3](https://github.com/sisong/HDiffPatch/tree/3b9dca715ca492873bf2c49e22e5d5b7d2a78620)
Commit: `3b9dca715ca492873bf2c49e22e5d5b7d2a78620`.

The six files under `HPatch/` are unmodified upstream files. `LICENSE` is the
upstream license. Only `patch.c` is compiled, with `_IS_USED_MULTITHREAD=0` and
`_HPATCH_IS_USED_MULTITHREAD=0`; no diff generator, compression plugins, thread
implementation, or command-line tools are included.

`tools/hpatch/main.c` uses `getSingleCompressedDiffInfo` and
`patch_single_compressed_diff`. The launcher currently accepts only the observed
official `HDIFFSF20` format with an empty compression type and zero compressed
size. Other formats fail explicitly. The helper streams old/diff files, limits
output to the manifest size, and caps the format's step memory at 64 MiB.
