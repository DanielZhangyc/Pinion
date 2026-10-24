// Package wine manages the launcher's independent Wine runtime and prefixes.
package wine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"pinion/internal/paths"
)

var ErrPrefixBusy = errors.New("Wine 容器仍有运行中的进程，无法准备或升级")

type Runtime struct {
	Root string
	id   string
}

// ID returns the version read from a validated runtime manifest.
func (r Runtime) ID() string { return r.id }

type manifest struct {
	Schema       int               `json:"schema"`
	ID           string            `json:"id"`
	Architecture string            `json:"architecture"`
	Backends     []string          `json:"backends"`
	Files        map[string]string `json:"files"`
}

// Discover only reads the launcher's installed runtime. It does not execute
// Wine, search applications, or select a different runtime after a failure.
func Discover(ctx context.Context) (Runtime, error) {
	if err := ctx.Err(); err != nil {
		return Runtime{}, err
	}
	layout, err := paths.Resolve()
	if err != nil {
		return Runtime{}, err
	}
	return Open(ctx, filepath.Join(layout.Data, "Runtime", "current"))
}

// Open validates an explicitly selected runtime, including staged builds used by
// developer probes. Production launch always uses Discover's installed path.
func Open(ctx context.Context, root string) (Runtime, error) {
	if err := ctx.Err(); err != nil {
		return Runtime{}, err
	}
	if !filepath.IsAbs(root) {
		return Runtime{}, errors.New("Wine 运行环境路径必须是绝对路径")
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Runtime{}, fmt.Errorf("读取独立 Wine 运行环境，请先安装 FineWine：%w", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "runtime.json"))
	if err != nil {
		return Runtime{}, fmt.Errorf("读取 Wine 运行环境清单：%w", err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Runtime{}, fmt.Errorf("解析 Wine 运行环境清单：%w", err)
	}
	if m.Schema != 1 || !validRuntimeID(m.ID) || m.Architecture != "x86_64" || !slices.Contains(m.Backends, "dxmt") || !slices.Contains(m.Backends, "vulkan") || len(m.Files) == 0 {
		return Runtime{}, errors.New("Wine 运行环境清单版本、架构或图形后端不符合要求")
	}
	for path, digest := range m.Files {
		decoded, err := hex.DecodeString(digest)
		if !filepath.IsLocal(path) || path == "." || strings.Contains(path, "\\") || err != nil || len(decoded) != sha256.Size {
			return Runtime{}, fmt.Errorf("Wine 运行环境清单包含无效文件或 SHA-256：%q", path)
		}
	}
	// Hash the launch path's critical binaries once per discovery, away from
	// rendering and process polling.
	for _, path := range []string{
		"bin/wine", "bin/wineserver", "lib/libMoltenVK.dylib", "libexec/gst-plugin-scanner",
		"lib/wine/x86_64-unix/wine",
		"lib/wine/x86_64-unix/ntdll.so", "lib/wine/x86_64-unix/winemac.so", "lib/wine/x86_64-unix/winevulkan.so",
		"lib/wine/x86_64-windows/ntdll.dll", "lib/wine/x86_64-windows/kernel32.dll", "lib/wine/x86_64-windows/wineboot.exe",
		"lib/wine/x86_64-windows/reg.exe", "lib/wine/x86_64-windows/cmd.exe",
		"lib/wine/x86_64-windows/winevulkan.dll", "lib/wine/x86_64-windows/ntoskrnl.exe", "lib/wine/x86_64-windows/lsass.exe",
		"lib/wine/x86_64-windows/wineusb.sys", "lib/wine/x86_64-unix/wineusb.so",
		"lib/wine/x86_64-unix/winemetal.so", "lib/wine/x86_64-windows/d3d10core.dll",
		"lib/wine/x86_64-windows/d3d11.dll", "lib/wine/x86_64-windows/dxgi.dll", "lib/wine/x86_64-windows/winemetal.dll",
	} {
		if err := ctx.Err(); err != nil {
			return Runtime{}, err
		}
		expected, ok := m.Files[path]
		if !ok {
			return Runtime{}, fmt.Errorf("Wine 运行环境清单缺少必需组件：%s", path)
		}
		if err := verifyFile(filepath.Join(root, filepath.FromSlash(path)), expected, strings.HasPrefix(path, "bin/") || strings.HasPrefix(path, "libexec/") || path == "lib/wine/x86_64-unix/wine"); err != nil {
			return Runtime{}, fmt.Errorf("检查 Wine 组件 %s：%w", path, err)
		}
	}
	for _, path := range []string{"share/wine", "share/wine/nls", "lib/gstreamer-deps", "lib/gstreamer-1.0"} {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return Runtime{}, fmt.Errorf("检查 Wine 数据目录 %s：%w", path, err)
		}
		if !info.IsDir() {
			return Runtime{}, fmt.Errorf("Wine 数据目录无效：%s", path)
		}
	}
	return Runtime{Root: root, id: m.ID}, nil
}

func validRuntimeID(id string) bool {
	if id == "" || len(id) > 128 || id == "current" || !(id[0] >= 'a' && id[0] <= 'z' || id[0] >= 'A' && id[0] <= 'Z' || id[0] >= '0' && id[0] <= '9') {
		return false
	}
	for _, char := range id {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' || char == '.') {
			return false
		}
	}
	return true
}

func verifyFile(path, expected string, executable bool) (resultErr error) {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || executable && info.Mode()&0111 == 0 {
		return errors.New("组件不是普通文件或缺少执行权限")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expected) {
		return errors.New("组件 SHA-256 与安装清单不一致")
	}
	return nil
}

func (r Runtime) environment(prefix string) []string {
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		polluted := false
		for _, family := range []string{"CX_", "WINE", "DYLD_", "GST_", "DXVK_", "DXMT_", "VK_", "MVK_", "D3DMETAL_", "METAL_", "MTL_", "MESA_", "PROTON_", "ROSETTA_", "__GL_"} {
			if strings.HasPrefix(key, family) {
				polluted = true
				break
			}
		}
		if !polluted {
			env = append(env, entry)
		}
	}
	return append(env,
		"WINEPREFIX="+prefix,
		"WINEARCH=win64",
		"WINELOADER="+filepath.Join(r.Root, "bin", "wine"),
		"WINESERVER="+filepath.Join(r.Root, "bin", "wineserver"),
		"WINEDLLPATH="+filepath.Join(r.Root, "lib", "wine"),
		"DYLD_FALLBACK_LIBRARY_PATH="+filepath.Join(r.Root, "lib")+":"+filepath.Join(r.Root, "lib", "wine", "x86_64-unix")+":/usr/lib",
		"GST_PLUGIN_SYSTEM_PATH_1_0="+filepath.Join(r.Root, "lib", "gstreamer-1.0"),
		"GST_PLUGIN_PATH_1_0="+filepath.Join(r.Root, "lib", "gstreamer-1.0"),
		"GST_PLUGIN_SCANNER_1_0="+filepath.Join(r.Root, "libexec", "gst-plugin-scanner"),
		"GST_REGISTRY_1_0="+filepath.Join(prefix, ".gryph-gstreamer-registry-x86_64.bin"),
		"WINEMSYNC=1", "ROSETTA_ADVERTISE_AVX=1", "WINEDEBUG=err+all,fixme-all",
	)
}

// Schema 2 requires prefix-wide RetinaMode in addition to the original settings.
const prefixSchema = 2

type prefixProfile struct {
	Schema           int    `json:"schema"`
	Backend          string `json:"backend"`
	RuntimeID        string `json:"runtimeId"`
	Ready            bool   `json:"ready"`
	PendingRuntimeID string `json:"pendingRuntimeId,omitempty"`
}

// Prepare initializes only a launcher-owned prefix and rejects backend changes.
// It never imports, resets, or terminates an existing CrossOver bottle.
func (r Runtime) Prepare(ctx context.Context, prefix, backend string, output io.Writer) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsAbs(prefix) || !filepath.IsAbs(r.Root) || r.id == "" {
		return errors.New("Wine 运行环境未验证，或容器路径不是绝对路径")
	}
	if backend != "dxmt" && backend != "vulkan" {
		return fmt.Errorf("不支持的 Wine 图形后端：%s", backend)
	}
	marker := filepath.Join(prefix, "gryph-prefix.json")
	if info, err := os.Lstat(prefix); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(prefix, 0700); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Wine 容器必须是独立的普通目录")
	}
	lock, err := os.OpenFile(filepath.Join(prefix, ".gryph-prepare.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, lock.Close()) }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return ErrPrefixBusy
		}
		return fmt.Errorf("锁定 Wine 容器准备流程：%w", err)
	}
	data, err := os.ReadFile(marker)
	if errors.Is(err, os.ErrNotExist) {
		entries, err := os.ReadDir(prefix)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() != ".gryph-prepare.lock" {
				return errors.New("现有 Wine 容器缺少 Pinion 容器标记（gryph-prefix.json），无法自动采用；请迁移用户数据至独立容器")
			}
		}
		data, err = json.Marshal(prefixProfile{Schema: prefixSchema, Backend: backend, RuntimeID: r.id})
		if err != nil {
			return err
		}
		if err := writeProfile(marker, prefixProfile{Schema: prefixSchema, Backend: backend, RuntimeID: r.id}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	var configured prefixProfile
	if err := json.Unmarshal(data, &configured); err != nil {
		return fmt.Errorf("读取 Wine 容器标记：%w", err)
	}
	if (configured.Schema != 1 && configured.Schema != prefixSchema) || configured.Backend != backend || !validRuntimeID(configured.RuntimeID) ||
		configured.PendingRuntimeID != "" && (configured.Ready || !validRuntimeID(configured.PendingRuntimeID) || configured.PendingRuntimeID == configured.RuntimeID) {
		return fmt.Errorf("Wine 容器标记或后端无效，当前为 %q，需要 %s", configured.Backend, backend)
	}
	if configured.PendingRuntimeID != "" && configured.PendingRuntimeID != r.id {
		return fmt.Errorf("Wine 容器尚未完成升级至 %q，请使用该版本重试；当前为 %q", configured.PendingRuntimeID, r.id)
	}
	upgrading := configured.RuntimeID != r.id
	preparing := !configured.Ready || upgrading
	configuring := preparing || configured.Schema < prefixSchema
	if configuring {
		if err := idlePrefix(prefix); err != nil {
			return err
		}
		var commands [][]string
		if preparing {
			boot := "--init"
			if upgrading {
				boot = "--update"
				configured.Ready = false
				configured.PendingRuntimeID = r.id
				if err := writeProfile(marker, configured); err != nil {
					return fmt.Errorf("记录 Wine 容器待升级状态：%w", err)
				}
			}
			commands = [][]string{
				{"wineboot.exe", boot},
				{"reg.exe", "add", `HKCU\Software\Wine`, "/v", "Version", "/t", "REG_SZ", "/d", "win11", "/f"},
				{"reg.exe", "add", `HKCU\Software\Wine\DllOverrides`, "/v", "d3d11", "/t", "REG_SZ", "/d", "builtin", "/f"},
				{"reg.exe", "add", `HKCU\Software\Wine\DllOverrides`, "/v", "dxgi", "/t", "REG_SZ", "/d", "builtin", "/f"},
			}
		}
		// An existing schema-1 Ready prefix needs only this setting. Keep its
		// marker at schema 1 until verification and hive persistence succeed so
		// a failed migration is retried without reinitializing user data.
		commands = append(commands, []string{"reg.exe", "add", `HKCU\Software\Wine\Mac Driver`, "/v", "RetinaMode", "/t", "REG_SZ", "/d", "y", "/f"})
		for _, args := range commands {
			cmd := exec.CommandContext(ctx, filepath.Join(r.Root, "bin", "wine"), args...)
			cmd.Env, cmd.Stdout, cmd.Stderr = r.environment(prefix), output, output
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("准备独立 Wine 容器（%s）失败，初始化尚未完成，可重试：%w", args[0], err)
			}
		}
		if err := r.verifyRegistry(ctx, prefix, output); err != nil {
			return err
		}
		// Registry commands update wineserver's in-memory hive. Wait for the
		// initialization services to exit and persist it before inspecting files
		// or publishing Ready; a fast first boot may not have system.reg yet.
		if err := r.Wait(ctx, prefix, output); err != nil {
			return err
		}
	}
	for _, name := range []string{"system.reg", "user.reg", "drive_c/windows/system32/cmd.exe", "drive_c/windows/system32/reg.exe", "drive_c/windows/system32/ntdll.dll", "drive_c/windows/system32/drivers/wineusb.sys", "drive_c/windows/inf/wineusb.inf"} {
		info, err := os.Stat(filepath.Join(prefix, name))
		if err != nil {
			return fmt.Errorf("Wine 容器不完整（%s）：%w", name, err)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return fmt.Errorf("Wine 容器文件无效：%s", name)
		}
	}
	root, err := filepath.EvalSymlinks(filepath.Join(prefix, "dosdevices", "z:"))
	if err != nil {
		return fmt.Errorf("读取 Wine Z: 映射：%w", err)
	}
	if root != "/" {
		return errors.New("Wine 容器的 Z: 映射已更改，无法访问游戏目录")
	}
	if configuring {
		configured.Schema = prefixSchema
		configured.RuntimeID = r.id
		configured.PendingRuntimeID = ""
		configured.Ready = true
		if err := writeProfile(marker, configured); err != nil {
			return fmt.Errorf("记录 Wine 容器准备完成状态：%w", err)
		}
	}
	return nil
}

// Match server/request.c's server directory and POSIX byte-range lock. Querying
// F_GETLK neither connects to Wine nor changes its lock or process lifetime.
func idlePrefix(prefix string) (resultErr error) {
	var stat syscall.Stat_t
	if err := syscall.Stat(prefix, &stat); err != nil {
		return fmt.Errorf("检查 Wine 容器目录：%w", err)
	}
	path := fmt.Sprintf("/tmp/.wine-%d/server-%x-%x/lock", os.Getuid(), uint64(stat.Dev), uint64(stat.Ino))
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取 Wine 服务锁：%w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	lock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: int16(io.SeekStart), Len: 1}
	if err := syscall.FcntlFlock(file.Fd(), syscall.F_GETLK, &lock); err != nil {
		return fmt.Errorf("检查 Wine 服务锁：%w", err)
	}
	if lock.Type != syscall.F_UNLCK {
		return ErrPrefixBusy
	}
	return nil
}

func (r Runtime) verifyRegistry(ctx context.Context, prefix string, output io.Writer) error {
	for _, query := range []struct{ key, name, expected string }{
		{`HKCU\Software\Wine`, "Version", "win11"},
		{`HKCU\Software\Wine\DllOverrides`, "d3d11", "builtin"},
		{`HKCU\Software\Wine\DllOverrides`, "dxgi", "builtin"},
		{`HKCU\Software\Wine\Mac Driver`, "RetinaMode", "y"},
	} {
		captured, err := r.queryRegistry(ctx, prefix, query.key, query.name, output)
		if err != nil {
			return fmt.Errorf("验证 Wine 注册表设置 %s：%w", query.name, err)
		}
		found := false
		for _, line := range strings.Split(string(captured), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 3 && strings.EqualFold(fields[0], query.name) && fields[1] == "REG_SZ" && fields[2] == query.expected {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("Wine 注册表设置 %s 未生效，需要 %s", query.name, query.expected)
		}
	}
	return nil
}

func (r Runtime) queryRegistry(ctx context.Context, prefix, key, name string, output io.Writer) (data []byte, resultErr error) {
	captured, err := os.CreateTemp("", "gryph-wine-registry-*")
	if err != nil {
		return nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, captured.Close(), os.Remove(captured.Name()))
	}()
	// Wine services can inherit stdout beyond reg.exe's lifetime. A real file
	// avoids an exec copy pipe whose EOF would depend on those services exiting.
	cmd := exec.CommandContext(ctx, filepath.Join(r.Root, "bin", "wine"), "reg.exe", "query", key, "/v", name)
	cmd.Env, cmd.Stdout, cmd.Stderr = r.environment(prefix), captured, output
	runErr := cmd.Run()
	data, readErr := os.ReadFile(captured.Name())
	var outputErr error
	if output != nil {
		// Run has finished copying stderr, so a caller's writer is never written
		// by stdout and stderr concurrently.
		_, outputErr = output.Write(data)
	}
	return data, errors.Join(runErr, readErr, outputErr)
}

func writeProfile(path string, profile prefixProfile) (resultErr error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".gryph-prefix-*")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			resultErr = errors.Join(resultErr, os.Remove(file.Name()))
		}
	}()
	if err := json.NewEncoder(file).Encode(profile); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	committed = true
	return nil
}

// Command deliberately leaves the game's lifetime independent of the launcher.
func (r Runtime) Command(prefix, executable string, args []string, output io.Writer) *exec.Cmd {
	cmd := exec.Command(filepath.Join(r.Root, "bin", "wine"), append([]string{executable}, args...)...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = filepath.Dir(executable), r.environment(prefix), output, output
	return cmd
}

// Wait waits for every process in this prefix without terminating any process.
func (r Runtime) Wait(ctx context.Context, prefix string, output io.Writer) error {
	cmd := exec.CommandContext(ctx, filepath.Join(r.Root, "bin", "wineserver"), "-w")
	cmd.Env, cmd.Stdout, cmd.Stderr = r.environment(prefix), output, output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("等待 Wine 容器进程退出：%w", err)
	}
	return nil
}
