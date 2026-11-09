// A narrow file adapter for the unmodified MIT-licensed HPatch core.
#include <errno.h>
#include <fcntl.h>
#include <inttypes.h>
#include <limits.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

#include "patch.h"

#define MAX_STEP_MEMORY ((hpatch_StreamPos_t)64 * 1024 * 1024)
#define IO_CACHE_SIZE (3 * 64 * 1024)

typedef struct {
    int fd;
    hpatch_StreamPos_t written;
} File;

static int fail(const char *message) {
    fprintf(stderr, "pinion-hpatch: %s\n", message);
    return 1;
}

static hpatch_BOOL read_input(const hpatch_TStreamInput *stream, hpatch_StreamPos_t position,
                              unsigned char *data, unsigned char *end) {
    const File *file = stream->streamImport;
    size_t size = (size_t)(end - data);
    if (position > stream->streamSize || size > stream->streamSize - position)
        return hpatch_FALSE;
    while (size != 0) {
        ssize_t count = pread(file->fd, data, size > INT_MAX ? INT_MAX : size, (off_t)position);
        if (count < 0 && errno == EINTR) continue;
        if (count <= 0) return hpatch_FALSE;
        data += count;
        position += (size_t)count;
        size -= (size_t)count;
    }
    return hpatch_TRUE;
}

static hpatch_BOOL write_output(const hpatch_TStreamOutput *stream, hpatch_StreamPos_t position,
                                const unsigned char *data, const unsigned char *end) {
    File *file = stream->streamImport;
    size_t size = (size_t)(end - data);
    if (position != file->written || position > stream->streamSize || size > stream->streamSize - position)
        return hpatch_FALSE;
    while (size != 0) {
        ssize_t count = write(file->fd, data, size > INT_MAX ? INT_MAX : size);
        if (count < 0 && errno == EINTR) continue;
        if (count <= 0) return hpatch_FALSE;
        data += count;
        file->written += (size_t)count;
        size -= (size_t)count;
    }
    return hpatch_TRUE;
}

static int open_input(const char *path, File *file, hpatch_TStreamInput *stream) {
    struct stat info;
    // O_NONBLOCK avoids waiting on a FIFO before the regular-file check.
    file->fd = open(path, O_RDONLY | O_CLOEXEC | O_NOFOLLOW | O_NONBLOCK);
    if (file->fd < 0) return fail("cannot open regular input without following symlinks");
    if (fstat(file->fd, &info) != 0 || !S_ISREG(info.st_mode) || info.st_size < 0)
        return fail("input is not a regular file");
    *stream = (hpatch_TStreamInput){file, (hpatch_StreamPos_t)info.st_size, read_input, NULL};
    return 0;
}

static int inspect(const hpatch_TStreamInput *diff, hpatch_singleCompressedDiffInfo *info) {
    memset(info, 0, sizeof(*info));
    if (!getSingleCompressedDiffInfo(info, diff, 0))
        return fail("invalid or unsupported HDIFFSF20 header");
    if (info->compressType[0] != '\0' || info->compressedSize != 0)
        return fail("compressed HDIFFSF20 patches are unsupported");
    if (info->newDataSize > INT64_MAX || info->oldDataSize > INT64_MAX)
        return fail("patch file sizes exceed supported range");
    if (info->diffDataPos > diff->streamSize || info->uncompressedSize != diff->streamSize - info->diffDataPos)
        return fail("patch payload length does not match header");
    if (info->stepMemSize > MAX_STEP_MEMORY)
        return fail("patch step memory exceeds 64 MiB limit");
    return 0;
}

int main(int argc, char **argv) {
    File old = {-1, 0}, diff = {-1, 0}, output = {-1, 0};
    hpatch_TStreamInput old_stream = {0}, diff_stream = {0};
    hpatch_singleCompressedDiffInfo info;
    unsigned char *cache = NULL;
    int result = 1;
    if (argc == 3 && strcmp(argv[1], "--info") == 0) {
        if (open_input(argv[2], &diff, &diff_stream) == 0 && inspect(&diff_stream, &info) == 0) {
            printf("new=%" PRIu64 " old=%" PRIu64 " step=%" PRIu64 "\n",
                   (uint64_t)info.newDataSize, (uint64_t)info.oldDataSize, (uint64_t)info.stepMemSize);
            result = 0;
        }
        if (diff.fd >= 0) close(diff.fd);
        return result;
    }
    if (argc != 5) return fail("usage: pinion-hpatch old diff new expected-size");
    char *size_end = NULL;
    errno = 0;
    uintmax_t expected = strtoumax(argv[4], &size_end, 10);
    if (argv[4][0] < '0' || argv[4][0] > '9' || !size_end || *size_end != '\0' || errno || expected > INT64_MAX)
        return fail("invalid expected output size");
    struct stat existing;
    if (lstat(argv[3], &existing) == 0 || errno != ENOENT)
        return fail("output path already exists or cannot be inspected");
    if (open_input(argv[1], &old, &old_stream) || open_input(argv[2], &diff, &diff_stream)) goto done;
    if (inspect(&diff_stream, &info)) goto done;
    if (info.newDataSize != expected) {
        fail("output size does not match expected manifest size");
        goto done;
    }
    if (info.oldDataSize != old_stream.streamSize) {
        fail("old file size does not match patch header");
        goto done;
    }
    size_t cache_size = (size_t)info.stepMemSize + IO_CACHE_SIZE;
    cache = malloc(cache_size);
    if (!cache) {
        fail("cannot allocate patch cache");
        goto done;
    }
    // Never truncate or replace an existing path, including a symlink.
    output.fd = open(argv[3], O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC, 0600);
    if (output.fd < 0) {
        fail("cannot exclusively create output");
        goto done;
    }
    hpatch_TStreamOutput new_stream = {&output, info.newDataSize, NULL, write_output};
    if (!patch_single_compressed_diff(&new_stream, &old_stream, &diff_stream,
                                     info.diffDataPos, info.uncompressedSize, 0, NULL,
                                     info.coverCount, (hpatch_size_t)info.stepMemSize,
                                     cache, cache + cache_size, NULL, 1) || output.written != expected) {
        fail("HDiff synthesis failed");
        goto done;
    }
    if (fsync(output.fd) != 0) {
        fail("cannot sync output");
        goto done;
    }
    result = 0;
done:
    free(cache);
    if (old.fd >= 0) close(old.fd);
    if (diff.fd >= 0) close(diff.fd);
    if (output.fd >= 0 && close(output.fd) != 0) result = fail("cannot close output");
    // The caller owns the staging directory, including partial output on failure
    // or process cancellation. Never unlink a path another process may replace.
    return result;
}
