// FetchToFile: the shared fetch-to-cache workflow — download a URL with an
// up-front disk-space guard (reclaiming prunable caches first), stream to a
// flock-guarded temp file under WeaveTmpDir while digesting, then atomically
// rename into place. Used by the IPSW retrieval (internal/vm) and the remote
// directory-archive share (internal/vm/run).
//go:build darwin

package storage

import (
	"context"
	"os"
	"path/filepath"

	weaveconfig "github.com/deploymenttheory/guestweave/internal/config"
	"github.com/deploymenttheory/guestweave/internal/fetcher"
	"github.com/deploymenttheory/guestweave/internal/fsutil"
	weavelock "github.com/deploymenttheory/guestweave/internal/lock"
	"github.com/deploymenttheory/guestweave/internal/logging"
	"github.com/deploymenttheory/guestweave/internal/oci"
)

// FetchToFileOptions controls FetchToFile's progress reporting.
type FetchToFileOptions struct {
	// AlwaysProgress starts the download-progress logger even when the
	// response carries no Content-Length (the IPSW path's historical
	// behaviour); otherwise progress appears only for known sizes.
	AlwaysProgress bool
}

// FetchToFile downloads url into a flock-guarded temp file under WeaveTmpDir
// (refusing up front if the volume can't hold Content-Length, reclaiming
// prunable caches first), then atomically renames it to finalPath(digest),
// where digest is the sha256 of the downloaded bytes ("sha256:…"). Returns
// the final path and the number of bytes written.
func FetchToFile(ctx context.Context, url string,
	finalPath func(digest string) string, opts FetchToFileOptions) (string, int64, error) {
	chunks, response, err := fetcher.FetcherFetch(ctx, fetcher.FetchRequest{URL: url}, true)
	if err != nil {
		return "", 0, err
	}

	config, err := weaveconfig.NewConfig()
	if err != nil {
		return "", 0, err
	}
	temporaryPath := filepath.Join(config.WeaveTmpDir, fsutil.UUID()+".download")

	// Refuse the download up front if the host volume cannot hold it
	// (prunable cache entries reclaimed first).
	var progress *logging.DownloadProgress
	if expectedLength := response.ContentLength; expectedLength > 0 {
		if err := EnsureDiskSpace(uint64(expectedLength), nil); err != nil {
			return "", 0, err
		}
	}
	if opts.AlwaysProgress || response.ContentLength > 0 {
		progress = logging.NewDownloadProgress(response.ContentLength)
		logging.NewProgressObserver(progress).Log(logging.DefaultLogger())
	}

	temporaryFile, err := os.Create(temporaryPath)
	if err != nil {
		return "", 0, err
	}
	defer temporaryFile.Close()

	lock, err := weavelock.NewFileLock(temporaryPath)
	if err != nil {
		return "", 0, err
	}
	defer lock.Close()
	if err := lock.Lock(); err != nil {
		return "", 0, err
	}

	var written int64
	digest := oci.NewDigest()
	for chunk := range chunks {
		if chunk.Err != nil {
			_ = os.Remove(temporaryPath)
			return "", 0, chunk.Err
		}
		if _, err := temporaryFile.Write(chunk.Data); err != nil {
			_ = os.Remove(temporaryPath)
			return "", 0, err
		}
		digest.Update(chunk.Data)
		written += int64(len(chunk.Data))
		if progress != nil {
			progress.Add(int64(len(chunk.Data)))
		}
	}
	if err := temporaryFile.Close(); err != nil {
		return "", 0, err
	}

	destination := finalPath(digest.Finalize())
	// Swift uses FileManager.replaceItemAt; an atomic rename is equivalent.
	if err := os.Rename(temporaryPath, destination); err != nil {
		return "", 0, err
	}
	return destination, written, nil
}
