package download

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type wirePack struct {
	URL  string `json:"url"`
	MD5  string `json:"md5"`
	Size string `json:"package_size"`
}

type patchDescriptor struct {
	Packs      []wirePack `json:"patches"`
	Password   string     `json:"cd_key"`
	InfoURL    string     `json:"v2_patch_info_url"`
	InfoSize   string     `json:"v2_patch_info_size"`
	InfoMD5    string     `json:"v2_patch_info_md5"`
	VerifyURL  string     `json:"v2_verify_files_url"`
	VerifySize string     `json:"v2_verify_files_size"`
	VerifyMD5  string     `json:"v2_verify_files_md5"`
}

type patchRecipe struct {
	archive, source, baseMD5 string
	baseSize                 int64
}

type patchPlan struct {
	oldVersion, oldManifest, password, identity string
	manifest                                    []byte
	v2                                          bool
	recipes                                     map[string]patchRecipe
	verify                                      struct {
		Move  []File `json:"move"`
		Patch []File `json:"patch"`
	}
	expanded int64
}

// ResolveUpdate uses only this launcher's completion record as the old version.
// A publisher's explicit null patch, or a distribution without differential
// support, selects the ordinary full package. Invalid metadata or an
// unsupported patch fails visibly instead of changing modes.
func (m *Manager) ResolveUpdate(ctx context.Context, game GameID) (Plan, error) {
	d, err := distributionFor(game)
	if err != nil {
		return Plan{}, err
	}
	root, err := m.openGame(game, false)
	if errors.Is(err, os.ErrNotExist) {
		return m.Resolve(ctx, game)
	}
	if err != nil {
		return Plan{}, err
	}
	defer root.Close()
	installed, err := loadRecord(root, game)
	if err != nil {
		// A missing file inside a recorded installation is damage, not a
		// publisher declaration that no differential package is available.
		if _, statErr := root.Lstat("completed.json"); errors.Is(statErr, os.ErrNotExist) {
			return m.Resolve(ctx, game)
		}
		return Plan{}, err
	}
	if !d.differential {
		return m.Resolve(ctx, game)
	}
	return m.resolveDifferential(ctx, game, installed.Version, installed.ManifestMD5)
}

func (m *Manager) officialPatch(ctx context.Context, game GameID, oldVersion string) (Plan, *patchDescriptor, error) {
	p := Plan{Game: game}
	d, err := distributionFor(game)
	if err != nil {
		return p, nil, err
	}
	if !d.differential {
		return p, nil, fmt.Errorf("differential updates are not enabled for %s", game)
	}
	if !validVersion(oldVersion) {
		return p, nil, errors.New("invalid differential base version")
	}
	body, err := d.latestGameBody(oldVersion)
	if err != nil {
		return p, nil, err
	}
	resp, err := m.request(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return p, nil, err
	}
	data, err := readBounded(resp, metadataLimit)
	if err != nil {
		return p, nil, err
	}
	var result struct {
		Responses []struct {
			Kind string `json:"kind"`
			Game struct {
				Version        string `json:"version"`
				RequestVersion string `json:"request_version"`
				Action         int    `json:"action"`
				State          int    `json:"state"`
				Package        struct {
					Packs    []wirePack `json:"packs"`
					FilePath string     `json:"file_path"`
					MD5      string     `json:"game_files_md5"`
				} `json:"pkg"`
				Patch *patchDescriptor `json:"patch"`
			} `json:"get_latest_game_rsp"`
		} `json:"proxy_rsps"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return p, nil, err
	}
	if len(result.Responses) != 1 || result.Responses[0].Kind != "get_latest_game" {
		return p, nil, errors.New("unexpected differential version response")
	}
	g := result.Responses[0].Game
	if g.RequestVersion != oldVersion || (g.Action != 0 && g.Action != 1) {
		return p, nil, errors.New("unexpected differential request version or action")
	}
	if g.State != 0 {
		return p, nil, fmt.Errorf("official game response state %d does not permit an update plan", g.State)
	}
	p.Version = g.Version
	if !validVersion(p.Version) {
		return p, nil, errors.New("invalid differential target version")
	}
	// A same-version reply and a version without a published differential can
	// legitimately have patch:null and an empty pkg. Resolve then validates a
	// separate full-package response instead of treating absent patch fields as
	// malformed differential metadata.
	if g.Patch == nil {
		return p, nil, nil
	}
	p.ManifestMD5 = g.Package.MD5
	p.ManifestURL = strings.TrimRight(g.Package.FilePath, "/") + "/game_files"
	if !validMD5(p.ManifestMD5) {
		return p, nil, errors.New("invalid differential target identity")
	}
	if err := checkURL(p.ManifestURL); err != nil {
		return p, nil, err
	}
	return p, g.Patch, nil
}

func (m *Manager) patchMetadata(ctx context.Context, raw, sizeText, want string) ([]byte, error) {
	size, err := strconv.ParseInt(sizeText, 10, 64)
	if err != nil || size <= 0 || size > metadataLimit || !validMD5(want) {
		return nil, errors.New("invalid differential metadata size or MD5")
	}
	resp, err := m.request(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	data, err := readBounded(resp, size)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size || sumMD5(data) != want {
		return nil, errors.New("differential metadata size or MD5 mismatch")
	}
	return data, nil
}

func (m *Manager) resolveDifferential(ctx context.Context, game GameID, oldVersion, oldManifest string) (Plan, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	p, d, err := m.officialPatch(ctx, game, oldVersion)
	if err != nil {
		return p, err
	}
	if d == nil {
		return m.Resolve(ctx, game)
	}
	if p.Version == oldVersion {
		return p, errors.New("publisher supplied a patch without a version change")
	}
	pp := &patchPlan{oldVersion: oldVersion, oldManifest: oldManifest, password: d.Password, recipes: map[string]patchRecipe{}}
	p.patch = pp
	p.Differential = true
	if pp.password == "" || len(d.Packs) == 0 {
		return p, errors.New("missing official patch volumes or password")
	}
	var identity strings.Builder
	for _, w := range d.Packs {
		size, e := strconv.ParseInt(w.Size, 10, 64)
		if e != nil || size <= 0 || size > 2<<30 || !validMD5(w.MD5) {
			return p, errors.New("invalid differential volume")
		}
		if e := checkURL(w.URL); e != nil {
			return p, e
		}
		p.Packs = append(p.Packs, Pack{w.URL, w.MD5, size})
		p.DownloadBytes += size
		if p.DownloadBytes > 1<<40 {
			return p, errors.New("differential download exceeds limit")
		}
		fmt.Fprintf(&identity, "%s:%d\n", w.MD5, size)
	}
	pp.identity = sumMD5([]byte(identity.String()))
	resp, err := m.request(ctx, http.MethodGet, p.ManifestURL, nil)
	if err != nil {
		return p, err
	}
	data, err := readBounded(resp, metadataLimit)
	if err != nil {
		return p, err
	}
	if sumMD5(data) != p.ManifestMD5 {
		return p, errors.New("target game_files MD5 mismatch")
	}
	p.ManifestBytes = int64(len(data))
	pp.manifest = data
	p.Files, err = decodeManifest(data)
	if err != nil {
		return p, err
	}
	target := map[string]File{}
	for _, f := range p.Files {
		if f.Size < 0 || f.Size > 1<<40 || !validMD5(f.MD5) {
			return p, errors.New("invalid target manifest file")
		}
		p.UnpackedBytes += f.Size
		if p.UnpackedBytes > 1<<40 {
			return p, errors.New("target expanded size exceeds limit")
		}
		target[f.Path] = f
	}
	remote := &remoteArchive{ctx: ctx, manager: m, packs: p.Packs, budget: 4 << 20}
	zr, err := zip.NewReader(remote, p.DownloadBytes)
	if err != nil {
		return p, fmt.Errorf("patch ZIP directory: %w", err)
	}
	p.archiveFiles, pp.expanded, err = inspectPatchArchive(zr)
	if err != nil {
		return p, err
	}
	archive := map[string]archiveItem{}
	for _, f := range p.archiveFiles {
		archive[f.name] = f
	}
	if d.InfoURL != "" {
		pp.v2 = true
		if d.VerifyURL == "" {
			return p, errors.New("missing v2 patch verification metadata")
		}
		info, e := m.patchMetadata(ctx, d.InfoURL, d.InfoSize, d.InfoMD5)
		if e != nil {
			return p, e
		}
		verify, e := m.patchMetadata(ctx, d.VerifyURL, d.VerifySize, d.VerifyMD5)
		if e != nil {
			return p, e
		}
		plain, e := decryptPatchMetadata(verify)
		if e != nil {
			return p, e
		}
		if e = json.Unmarshal(plain, &pp.verify); e != nil {
			return p, e
		}
		if e = parseV2Patch(info, p.Version, target, archive, pp); e != nil {
			return p, e
		}
	} else if d.VerifyURL != "" {
		return p, errors.New("v2 verification metadata without patch map")
	}
	for _, f := range p.archiveFiles {
		if f.dir {
			continue
		}
		if t, ok := target[f.name]; ok {
			if _, exists := pp.recipes[f.name]; exists {
				return p, fmt.Errorf("conflicting patch destination %q", f.name)
			}
			if t.Size != f.size {
				return p, fmt.Errorf("patch replacement size mismatch for %q", f.name)
			}
			pp.recipes[f.name] = patchRecipe{archive: f.name}
		}
	}
	if len(pp.recipes) == 0 {
		return p, errors.New("official patch has no target file operations")
	}
	if err := validatePatchVerify(target, pp); err != nil {
		return p, err
	}
	p.RequiredBytes = p.UnpackedBytes + pp.expanded + p.DownloadBytes + p.ManifestBytes
	if p.RequiredBytes > 3<<40 {
		return p, errors.New("differential required space exceeds limit")
	}
	return p, p.validate(true)
}

func inspectPatchArchive(zr *zip.Reader) ([]archiveItem, int64, error) {
	var items []archiveItem
	var total int64
	seen := map[string]bool{}
	for _, f := range zr.File {
		name, err := safeName(f.Name)
		if err != nil {
			return nil, 0, err
		}
		if seen[strings.ToLower(name)] {
			return nil, 0, fmt.Errorf("duplicate patch ZIP path %q", name)
		}
		seen[strings.ToLower(name)] = true
		if f.Mode()&os.ModeSymlink != 0 || (!f.FileInfo().IsDir() && !f.Mode().IsRegular()) {
			return nil, 0, fmt.Errorf("unsupported patch ZIP entry %q", name)
		}
		if err := validatePatchFile(f); err != nil {
			return nil, 0, err
		}
		if f.UncompressedSize64 > 1<<40 {
			return nil, 0, errors.New("patch ZIP entry too large")
		}
		total += int64(f.UncompressedSize64)
		if total > 1<<40 {
			return nil, 0, errors.New("patch ZIP expanded size exceeds limit")
		}
		items = append(items, archiveItem{name, int64(f.UncompressedSize64), f.CRC32, f.FileInfo().IsDir()})
	}
	if len(items) == 0 {
		return nil, 0, errors.New("empty patch ZIP")
	}
	return items, total, nil
}

func decryptPatchMetadata(data []byte) ([]byte, error) {
	key, err := hex.DecodeString("c0f30e1ce763bbc21cc355a34303ac50399444bff68c4a22af398c0a166ee143")
	if err != nil {
		return nil, err
	}
	iv, err := hex.DecodeString("33467861192750649501937264608400")
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data)%block.BlockSize() != 0 {
		return nil, errors.New("invalid encrypted patch metadata length")
	}
	plain := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, data)
	pad := int(plain[len(plain)-1])
	if pad < 1 || pad > block.BlockSize() || !bytes.Equal(plain[len(plain)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		return nil, errors.New("invalid patch metadata padding")
	}
	return plain[:len(plain)-pad], nil
}

func parseV2Patch(data []byte, version string, target map[string]File, archive map[string]archiveItem, pp *patchPlan) error {
	var info struct {
		Version string `json:"version"`
		Base    string `json:"vfs_base_path"`
		Files   []struct {
			Name     string `json:"name"`
			NamePath string `json:"name_path"`
			MD5      string `json:"md5"`
			Size     int64  `json:"size"`
			DiffType int    `json:"diffType"`
			Local    string `json:"local_path"`
			Patch    []struct {
				BaseFile  string `json:"base_file"`
				BasePath  string `json:"base_file_path"`
				BaseMD5   string `json:"base_md5"`
				BaseSize  int64  `json:"base_size"`
				Patch     string `json:"patch"`
				PatchPath string `json:"patch_path"`
				PatchSize int64  `json:"patch_size"`
			} `json:"patch"`
		} `json:"files"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return err
	}
	if info.Version != version {
		return errors.New("v2 patch version mismatch")
	}
	if _, err := safeName(info.Base); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, f := range info.Files {
		if f.NamePath != "" || (f.DiffType != 1 && f.DiffType != 2) {
			return errors.New("unsupported v2 patch path or diffType")
		}
		if _, err := safeName(f.Name); err != nil {
			return err
		}
		name := path.Join(info.Base, f.Name)
		t, ok := target[name]
		if !ok || t.Size != f.Size || t.MD5 != f.MD5 {
			return fmt.Errorf("v2 target disagrees with full manifest: %q", name)
		}
		if seen[strings.ToLower(name)] {
			return fmt.Errorf("duplicate v2 target %q", name)
		}
		seen[strings.ToLower(name)] = true
		if f.Local != "" && len(f.Patch) != 0 {
			return errors.New("ambiguous v2 patch operation")
		}
		if f.Local != "" {
			if _, err := safeName(f.Local); err != nil {
				return err
			}
			a, ok := archive[f.Local]
			if !ok || a.dir || a.size != f.Size {
				return fmt.Errorf("missing v2 replacement %q", f.Local)
			}
			pp.recipes[name] = patchRecipe{archive: f.Local}
		} else if len(f.Patch) > 0 {
			if len(f.Patch) != 1 {
				return errors.New("unsupported multiple v2 patch bases")
			}
			d := f.Patch[0]
			if d.BasePath != "" || d.PatchPath != "" || !validMD5(d.BaseMD5) || d.BaseSize < 0 || d.BaseSize > 1<<40 || d.PatchSize < 0 {
				return errors.New("unsupported v2 patch base")
			}
			if _, err := safeName(d.BaseFile); err != nil {
				return err
			}
			if _, err := safeName(d.Patch); err != nil {
				return err
			}
			aName := path.Join("vfs_files/vfs_patch", d.Patch)
			a, ok := archive[aName]
			if !ok || a.dir || a.size != d.PatchSize {
				return fmt.Errorf("missing v2 binary patch %q", aName)
			}
			pp.recipes[name] = patchRecipe{aName, path.Join(info.Base, d.BaseFile), d.BaseMD5, d.BaseSize}
		}
	}
	return nil
}

func validatePatchVerify(target map[string]File, pp *patchPlan) error {
	seen := map[string]bool{}
	for _, list := range [][]File{pp.verify.Move, pp.verify.Patch} {
		for _, f := range list {
			if _, err := safeName(f.Path); err != nil {
				return err
			}
			if seen[strings.ToLower(f.Path)] {
				return fmt.Errorf("duplicate patch verification path %q", f.Path)
			}
			seen[strings.ToLower(f.Path)] = true
			// The official Arknights patch verifies its empty encrypted
			// game_files as an archive artifact, not as the final manifest.
			if f.Path == "game_files" {
				if f.Size < 0 || f.Size > metadataLimit || !validMD5(f.MD5) {
					return errors.New("invalid patch game_files identity")
				}
				continue
			}
			t, ok := target[f.Path]
			if !ok || f.Size != t.Size || f.MD5 != t.MD5 {
				return fmt.Errorf("patch verification disagrees with full manifest: %q", f.Path)
			}
			if _, ok := pp.recipes[f.Path]; !ok {
				return fmt.Errorf("missing patch verification operation %q", f.Path)
			}
		}
	}
	if pp.v2 {
		for name := range pp.recipes {
			if !seen[strings.ToLower(name)] {
				return fmt.Errorf("v2 operation is absent from verification metadata: %q", name)
			}
		}
	}
	return nil
}

func patchCacheBase(base string, pp *patchPlan) string {
	return path.Join(base, "differential-"+pp.identity)
}

func (m *Manager) installPatch(ctx context.Context, root *os.Root, p Plan, base string, old Record, emit func(string, string, int64, int64)) (Record, error) {
	var empty Record
	pp := p.patch
	cache := patchCacheBase(base, pp)
	for _, dir := range []string{path.Join(base, "game"), path.Join(base, "patch-staging"), path.Join(cache, "cache")} {
		if err := makeDirs(root, dir); err != nil {
			return empty, err
		}
	}
	// Remove only the known unfinished outputs before free-space accounting.
	for i := range p.Files {
		tmp := path.Join(base, "patch-staging", fmt.Sprintf("%d.tmp", i))
		for _, name := range []string{tmp, tmp + ".diff"} {
			if err := regular(root, name); err != nil {
				return empty, err
			}
			if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
				return empty, err
			}
		}
	}
	required := p.RequiredBytes
	for _, f := range p.Files {
		ok, err := candidateMatches(ctx, root, path.Join(base, "game", f.Path), f)
		if err != nil {
			return empty, err
		}
		if ok {
			required -= f.Size
		}
	}
	for i, pack := range p.Packs {
		for _, suffix := range []string{"", ".part"} {
			name := packName(cache, i) + suffix
			if err := regular(root, name); err != nil {
				return empty, err
			}
			info, err := root.Lstat(name)
			if err == nil {
				if info.Size() > pack.Size {
					return empty, errors.New("patch cache exceeds declared size")
				}
				required -= info.Size()
				break
			}
			if !errors.Is(err, os.ErrNotExist) {
				return empty, err
			}
		}
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(root.Name(), &stat); err != nil {
		return empty, err
	}
	if uint64(max(required, 0)) > uint64(stat.Bavail)*uint64(stat.Bsize) {
		return empty, fmt.Errorf("insufficient disk space: need %d bytes", required)
	}
	var done int64
	for i, pack := range p.Packs {
		// Signed URLs can expire while a previous volume downloads. Refresh
		// before every volume, preserving the MD5-qualified partial bytes.
		fresh, err := m.refreshPatchPacks(ctx, p)
		if err != nil {
			return empty, err
		}
		p.Packs = fresh
		pack = fresh[i]
		name := packName(cache, i)
		emit(StageDownloading, path.Base(name), done, p.DownloadBytes)
		if err := m.fetchPack(ctx, root, pack, name, func(n int64) { emit(StageDownloading, path.Base(name), done+n, p.DownloadBytes) }); err != nil {
			return empty, err
		}
		done += pack.Size
	}
	local := &localArchive{root: root, packs: p.Packs, base: cache}
	defer local.Close()
	zr, err := zip.NewReader(local, p.DownloadBytes)
	if err != nil {
		return empty, err
	}
	items, expanded, err := inspectPatchArchive(zr)
	if err != nil {
		return empty, err
	}
	if expanded != pp.expanded || len(items) != len(p.archiveFiles) {
		return empty, errors.New("patch ZIP directory changed")
	}
	for i, f := range items {
		if f != p.archiveFiles[i] {
			return empty, errors.New("patch ZIP entry changed")
		}
	}
	entries := map[string]*zip.File{}
	for _, f := range zr.File {
		n, _ := safeName(f.Name)
		entries[n] = f
	}
	if err := checkPackageFiles(ctx, entries, pp.password, p); err != nil {
		return empty, err
	}
	done = 0
	emit(StageExtracting, "", 0, p.UnpackedBytes)
	for i, f := range p.Files {
		dest := path.Join(base, "game", f.Path)
		ok, err := candidateMatches(ctx, root, dest, f)
		if err != nil {
			return empty, err
		}
		if ok {
			done += f.Size
			emit(StageExtracting, f.Path, done, p.UnpackedBytes)
			continue
		}
		if err := makeDirs(root, path.Dir(dest)); err != nil {
			return empty, err
		}
		tmp := path.Join(base, "patch-staging", fmt.Sprintf("%d.tmp", i))
		diff := tmp + ".diff"
		for _, name := range []string{tmp, diff} {
			if err := regular(root, name); err != nil {
				return empty, err
			}
			if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
				return empty, err
			}
		}
		r, changed := pp.recipes[f.Path]
		if !changed {
			err = copyPatchBase(ctx, root, path.Join(recordBase(old), "game", f.Path), tmp, f)
		} else if r.source == "" {
			err = extractPatchOne(ctx, root, entries[r.archive], pp.password, tmp)
		} else {
			source := path.Join(recordBase(old), "game", r.source)
			if err = verifyFile(ctx, root, source, r.baseSize, r.baseMD5); err == nil {
				err = extractPatchOne(ctx, root, entries[r.archive], pp.password, diff)
			}
			if err == nil {
				err = applyHDiff(ctx, filepath.Join(root.Name(), source), filepath.Join(root.Name(), diff), filepath.Join(root.Name(), tmp), f.Size)
			}
			removeErr := root.Remove(diff)
			if !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, removeErr)
			}
		}
		if err != nil {
			return empty, fmt.Errorf("patch %s: %w", f.Path, err)
		}
		if err := verifyFile(ctx, root, tmp, f.Size, f.MD5); err != nil {
			return empty, err
		}
		if err := regular(root, dest); err != nil {
			return empty, err
		}
		if err := root.Rename(tmp, dest); err != nil {
			return empty, err
		}
		done += f.Size
		emit(StageExtracting, f.Path, done, p.UnpackedBytes)
	}
	if err := writePatchSidecars(ctx, root, p, base, entries); err != nil {
		return empty, err
	}
	if err := local.Close(); err != nil {
		return empty, err
	}
	if err := verifyPlan(ctx, root, p, base, emit); err != nil {
		return empty, err
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	return commitRecord(root, p, base, emit)
}

func writePatchSidecars(ctx context.Context, root *os.Root, p Plan, base string, entries map[string]*zip.File) error {
	if int64(len(p.patch.manifest)) != p.ManifestBytes || sumMD5(p.patch.manifest) != p.ManifestMD5 {
		return errors.New("target manifest identity changed")
	}
	packageFiles, err := readPatchMetadata(ctx, entries["package_files"], p.patch.password)
	if err != nil {
		return err
	}
	gameFiles, err := readPatchMetadata(ctx, entries["game_files"], p.patch.password)
	if err != nil {
		return err
	}
	// The archive's game_files is an official game artifact (empty encrypted
	// bytes for Arknights), distinct from the complete external verification
	// manifest. Keep the latter beside the version's launcher metadata.
	for _, artifact := range []struct {
		name string
		data []byte
		md5  string
	}{
		{"target-game_files", p.patch.manifest, p.ManifestMD5},
		{"game/game_files", gameFiles, sumMD5(gameFiles)},
		{"game/package_files", packageFiles, sumMD5(packageFiles)},
	} {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := path.Join(base, artifact.name)
		if err := atomicWrite(root, name, artifact.data); err != nil {
			return err
		}
		if err := verifyFile(ctx, root, name, int64(len(artifact.data)), artifact.md5); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) refreshPatchPacks(ctx context.Context, p Plan) ([]Pack, error) {
	fresh, d, err := m.officialPatch(ctx, p.Game, p.patch.oldVersion)
	if err != nil {
		return nil, err
	}
	if d == nil || fresh.Version != p.Version || fresh.ManifestMD5 != p.ManifestMD5 || len(d.Packs) != len(p.Packs) || d.Password != p.patch.password {
		return nil, errors.New("official differential package identity changed")
	}
	packs := make([]Pack, len(d.Packs))
	for i, w := range d.Packs {
		size, err := strconv.ParseInt(w.Size, 10, 64)
		if err != nil || size != p.Packs[i].Size || w.MD5 != p.Packs[i].MD5 {
			return nil, errors.New("official differential volume identity changed")
		}
		if err := checkURL(w.URL); err != nil {
			return nil, err
		}
		packs[i] = Pack{w.URL, w.MD5, size}
	}
	return packs, nil
}

func candidateMatches(ctx context.Context, root *os.Root, name string, f File) (bool, error) {
	err := verifyFile(ctx, root, name, f.Size, f.MD5)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrChecksum) {
		return false, nil
	}
	return err == nil, err
}

func copyPatchBase(ctx context.Context, root *os.Root, source, dest string, f File) error {
	if err := verifyFile(ctx, root, source, f.Size, f.MD5); err != nil {
		return err
	}
	in, err := root.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := root.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, &contextReader{ctx: ctx, r: in})
	return errors.Join(copyErr, out.Close())
}

func extractPatchOne(ctx context.Context, root *os.Root, f *zip.File, password, dest string) error {
	if f == nil {
		return errors.New("missing patch ZIP entry")
	}
	in, err := openPatchFile(f, password)
	if err != nil {
		return err
	}
	out, err := root.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return errors.Join(err, in.Close())
	}
	n, copyErr := io.Copy(out, &contextReader{ctx: ctx, r: in})
	closeErr := errors.Join(in.Close(), out.Close())
	if err := errors.Join(copyErr, closeErr); err != nil {
		return err
	}
	if uint64(n) != f.UncompressedSize64 {
		return errors.New("patch extracted size mismatch")
	}
	return nil
}

func readPatchMetadata(ctx context.Context, f *zip.File, password string) ([]byte, error) {
	if f == nil || f.UncompressedSize64 > uint64(metadataLimit) {
		return nil, errors.New("missing or excessive patch metadata")
	}
	r, err := openPatchFile(f, password)
	if err != nil {
		return nil, err
	}
	b, readErr := io.ReadAll(&contextReader{ctx: ctx, r: io.LimitReader(r, metadataLimit+1)})
	return b, errors.Join(readErr, r.Close())
}

// package_files is an additional, authenticated list of replacements. The full
// target manifest remains authoritative (the Arknights patch's game_files is empty).
func checkPackageFiles(ctx context.Context, entries map[string]*zip.File, password string, p Plan) error {
	data, err := readPatchMetadata(ctx, entries["package_files"], password)
	if err != nil {
		return err
	}
	plain, err := decryptPatchMetadata(data)
	if err != nil {
		return err
	}
	target := map[string]File{}
	for _, f := range p.Files {
		target[f.Path] = f
	}
	scanner := bufio.NewScanner(bytes.NewReader(plain))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	seen := map[string]bool{}
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var f File
		if err := json.Unmarshal(scanner.Bytes(), &f); err != nil {
			return err
		}
		if _, err := safeName(f.Path); err != nil {
			return err
		}
		if seen[strings.ToLower(f.Path)] {
			return errors.New("duplicate package_files entry")
		}
		seen[strings.ToLower(f.Path)] = true
		if f.Path == "game_files" {
			if err := verifyPatchArtifact(ctx, entries[f.Path], password, f); err != nil {
				return err
			}
			continue
		}
		t, ok := target[f.Path]
		if !ok || f.Size != t.Size || f.MD5 != t.MD5 {
			return fmt.Errorf("package_files disagrees with target manifest: %q", f.Path)
		}
		if _, ok := p.patch.recipes[f.Path]; !ok {
			return errors.New("package_files has no replacement")
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	for _, list := range [][]File{p.patch.verify.Move, p.patch.verify.Patch} {
		for _, f := range list {
			if f.Path == "game_files" {
				if err := verifyPatchArtifact(ctx, entries[f.Path], password, f); err != nil {
					return err
				}
			}
		}
	}
	if f, ok := entries["delete_files.txt"]; ok {
		b, err := readPatchMetadata(ctx, f, password)
		if err != nil {
			return err
		}
		s := bufio.NewScanner(bytes.NewReader(b))
		for s.Scan() {
			n := strings.TrimSpace(s.Text())
			if n == "" {
				continue
			}
			if _, err := safeName(n); err != nil {
				return err
			}
			if _, exists := target[n]; exists {
				return fmt.Errorf("delete_files includes target path %q", n)
			}
		}
		if err := s.Err(); err != nil {
			return err
		}
	}
	return nil
}

func verifyPatchArtifact(ctx context.Context, f *zip.File, password string, expected File) error {
	if !validMD5(expected.MD5) || expected.Size < 0 || expected.Size > metadataLimit {
		return errors.New("invalid patch artifact identity")
	}
	b, err := readPatchMetadata(ctx, f, password)
	if err != nil {
		return err
	}
	if int64(len(b)) != expected.Size || sumMD5(b) != expected.MD5 {
		return errors.New("patch artifact size or MD5 mismatch")
	}
	return nil
}
