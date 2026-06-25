//go:build darwin

package winimage

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	sdapi "github.com/deploymenttheory/go-sdk-winmediafoundry/softwaredownload/api/softwaredownload"
	sdconst "github.com/deploymenttheory/go-sdk-winmediafoundry/softwaredownload/constants"
	swdl "github.com/deploymenttheory/go-sdk-winmediafoundry/softwaredownload"
	"github.com/deploymenttheory/go-sdk-winmediafoundry/pkg/iso"
)

// acquireSWDL implements the software-download acquisition path: it downloads
// the latest official Windows 11 ARM64 ISO from Microsoft's software-download
// site, and — when opts.Unattend is set — re-masters it with autounattend.xml
// injected at the root.
func acquireSWDL(ctx context.Context, opts Options) (*Result, error) {
	dlDir := filepath.Join(opts.CacheDir, "iso", "swdl")
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		return nil, fmt.Errorf("winimage: create cache dir: %w", err)
	}

	// Build the softwaredownload call options.
	callOpts := []sdapi.Option{
		sdapi.WithArch(sdconst.ArchARM64),
		sdapi.WithLanguage(normalizeLanguage(opts.Language)),
		sdapi.WithDownloadDir(dlDir),
	}
	if opts.Progress != nil {
		callOpts = append(callOpts, sdapi.WithProgress(opts.Progress))
	}

	logf(opts.Progress, "winimage: resolving latest Windows 11 ARM64 ISO from Microsoft…\n")

	client, err := swdl.NewClient()
	if err != nil {
		return nil, fmt.Errorf("winimage: create softwaredownload client: %w", err)
	}

	link, _, err := client.GetByName(ctx, "Arm64", callOpts...)
	if err != nil {
		return nil, fmt.Errorf("winimage: resolve Windows 11 ARM64 ISO: %w", err)
	}
	officialISO := link.LocalPath
	if officialISO == "" {
		return nil, fmt.Errorf("winimage: download did not produce a local file")
	}

	release := releaseFromFileName(link.FileName)

	if len(opts.Unattend) == 0 {
		return &Result{
			ISOPath:  officialISO,
			Build:    0,
			Release:  release,
			Edition:  opts.Edition,
			FileName: link.FileName,
		}, nil
	}

	// Build the re-mastered ISO with autounattend.xml injected.
	uaISO, err := buildUnattendISO(ctx, officialISO, link.FileName, opts)
	if err != nil {
		return nil, err
	}
	uaFile := filepath.Base(uaISO)
	return &Result{
		ISOPath:  uaISO,
		Build:    0,
		Release:  release,
		Edition:  opts.Edition,
		FileName: uaFile,
	}, nil
}

// buildUnattendISO extracts officialISO, injects autounattend.xml from
// opts.Unattend, and re-masters the result. The output is cached under
// cacheDir/iso/swdl/ keyed by the unattend content hash so a fresh ISO is
// rebuilt whenever the autounattend.xml changes.
func buildUnattendISO(ctx context.Context, officialISO, officialFileName string, opts Options) (string, error) {
	sum := sha1.Sum(opts.Unattend)
	base := strings.TrimSuffix(officialFileName, filepath.Ext(officialFileName))
	uaName := fmt.Sprintf("%s-ua%s-v%d.iso", base, hex.EncodeToString(sum[:4]), isoFormatVersion)
	uaISO := filepath.Join(filepath.Dir(officialISO), uaName)

	if info, err := os.Stat(uaISO); err == nil && info.Size() > 0 {
		logf(opts.Progress, "winimage: using cached unattended ISO %s\n", uaName)
		return uaISO, nil
	}

	logf(opts.Progress, "winimage: extracting %s…\n", officialFileName)
	extractDir, err := extractISO(officialISO, opts.CacheDir)
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(extractDir)

	// Inject autounattend.xml at the media root so Windows Setup discovers it.
	if err := os.WriteFile(filepath.Join(extractDir, "autounattend.xml"), opts.Unattend, 0o644); err != nil {
		return "", fmt.Errorf("winimage: inject autounattend.xml: %w", err)
	}

	logf(opts.Progress, "winimage: re-mastering ISO with autounattend.xml…\n")
	tmpISO := uaISO + ".tmp"
	if err := iso.BuildWindowsUDF(extractDir, tmpISO, arm64VolumeID); err != nil {
		_ = os.Remove(tmpISO)
		return "", fmt.Errorf("winimage: re-master ISO: %w", err)
	}
	if err := os.Rename(tmpISO, uaISO); err != nil {
		_ = os.Remove(tmpISO)
		return "", fmt.Errorf("winimage: finalize re-mastered ISO: %w", err)
	}

	logf(opts.Progress, "winimage: unattended ISO ready at %s\n", uaISO)
	return uaISO, nil
}

// extractISO mounts isoPath with hdiutil and copies the entire contents to a
// temporary directory under tempParent. The caller must os.RemoveAll the
// returned directory when done.
func extractISO(isoPath, tempParent string) (string, error) {
	extractDir, err := os.MkdirTemp(tempParent, "iso-extract-")
	if err != nil {
		return "", fmt.Errorf("winimage: create extract dir: %w", err)
	}
	mountDir, err := os.MkdirTemp("", "weave-iso-mount-")
	if err != nil {
		os.RemoveAll(extractDir)
		return "", fmt.Errorf("winimage: create mount dir: %w", err)
	}
	defer os.RemoveAll(mountDir)

	if out, err := exec.Command(
		"hdiutil", "attach",
		"-readonly", "-nobrowse",
		"-mountpoint", mountDir,
		isoPath,
	).CombinedOutput(); err != nil {
		os.RemoveAll(extractDir)
		return "", fmt.Errorf("winimage: hdiutil attach: %w: %s", err, strings.TrimSpace(string(out)))
	}

	// Ensure we detach even if the copy fails.
	detach := func() {
		_ = exec.Command("hdiutil", "detach", "-quiet", mountDir).Run()
	}
	defer detach()

	// cp -R <mount>/. <extract> copies all content including hidden files.
	if out, err := exec.Command("cp", "-R", mountDir+"/.", extractDir).CombinedOutput(); err != nil {
		os.RemoveAll(extractDir)
		return "", fmt.Errorf("winimage: copy ISO content: %w: %s", err, strings.TrimSpace(string(out)))
	}

	return extractDir, nil
}

// normalizeLanguage maps common BCP-47 locale strings to the localised
// language name Microsoft's software-download connector expects. Unrecognised
// values are passed through unchanged (the connector does substring matching,
// so "en-us" already works).
func normalizeLanguage(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "en-us", "en_us", "english":
		return "English (United States)"
	case "en-gb", "en_gb":
		return "English (United Kingdom)"
	case "de-de", "de_de", "german":
		return "German"
	case "fr-fr", "fr_fr", "french":
		return "French"
	case "es-es", "es_es", "spanish":
		return "Spanish"
	case "it-it", "it_it", "italian":
		return "Italian"
	case "ja-jp", "ja_jp", "japanese":
		return "Japanese"
	case "zh-cn", "zh_cn", "chinese simplified":
		return "Chinese Simplified"
	case "zh-tw", "zh_tw", "chinese traditional":
		return "Chinese Traditional"
	case "ko-kr", "ko_kr", "korean":
		return "Korean"
	case "pt-br", "pt_br", "portuguese brazil":
		return "Portuguese (Brazil)"
	default:
		return lang
	}
}

// releaseFromFileName extracts the Windows release label from an ISO filename
// such as "Win11_25H2_English_Arm64_v2.iso" → "25H2". Returns "latest" when
// the filename does not follow the expected pattern.
func releaseFromFileName(fileName string) string {
	name := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	parts := strings.SplitN(name, "_", 3)
	if len(parts) >= 2 && parts[0] == "Win11" {
		return parts[1]
	}
	return "latest"
}
