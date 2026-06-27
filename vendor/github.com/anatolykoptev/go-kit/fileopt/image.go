package fileopt

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

const imageOptimizeTimeout = 30 * time.Second

// OptimizePNG losslessly recompresses a PNG via oxipng. Input bytes must be
// a valid PNG; output is a (typically smaller) valid PNG. Missing oxipng →
// returns original bytes + warn log.
func OptimizePNG(ctx context.Context, data []byte) ([]byte, error) {
	bin := resolveBinary("FILEOPT_OXIPNG_PATH", "oxipng")
	// Missing resolver OR stale env-override path → graceful no-op.
	if bin == "" || !binaryExists(bin) {
		RecordSkipped(StageOxiPNG)
		slog.Warn("fileopt: oxipng not available; returning original PNG", "bytes", len(data))
		return data, nil
	}
	// -o 4 → default. A/B on 9 fixtures (solid/gradient/noise × 64/500/1920)
	// showed -o 6 adds 0-4 bytes (0.00-0.05% ratio) for 1-761ms extra CPU —
	// not worth it. Upgrade only if future content shows meaningful gain.
	// --strip all → remove tIME/iTXt/eXIf/metadata (safe for agent-generated PNGs
	//   that never carry legal/semantic metadata).
	return runImageTool(ctx, bin, data, "png", StageOxiPNG,
		[]string{"-o", "4", "--strip", "all", "--quiet"},
		// oxipng: oxipng <extraArgs> <in> --out <out>
		inOutArgs{InFirst: true, OutFlag: "--out"},
	)
}

// OptimizeWebP encodes bytes (PNG/JPEG input) as WebP at the given quality
// (1-100). Quality 80 is a common sweet spot for UI-card style images.
func OptimizeWebP(ctx context.Context, data []byte, quality int) ([]byte, error) {
	if quality < 1 || quality > 100 {
		return nil, fmt.Errorf("fileopt: webp quality must be 1..100, got %d", quality)
	}
	bin := resolveBinary("FILEOPT_CWEBP_PATH", "cwebp")
	// Missing resolver OR stale env-override path → graceful no-op.
	if bin == "" || !binaryExists(bin) {
		RecordSkipped(StageCwebp)
		slog.Warn("fileopt: cwebp not available; returning original bytes", "bytes", len(data))
		return data, nil
	}
	return runImageTool(ctx, bin, data, "webp", StageCwebp,
		[]string{"-q", strconv.Itoa(quality), "-m", "6", "-quiet"},
		// cwebp: cwebp <extraArgs> <in> -o <out>
		inOutArgs{InFirst: true, OutFlag: "-o"},
	)
}

// inOutArgs encodes where the in/out paths go in the command.
type inOutArgs struct {
	InFirst bool   // true = in path before -o out
	OutFlag string // flag that precedes the out path
}

// runImageTool is a common harness: write bytes to a temp file, run the
// command with appropriate in/out paths, read output, record metrics.
func runImageTool(ctx context.Context, bin string, data []byte, outExt, stage string, extraArgs []string, io inOutArgs) ([]byte, error) {
	dir, err := os.MkdirTemp("", "fileopt-img-*")
	if err != nil {
		return nil, fmt.Errorf("fileopt: mkdir temp: %w", err)
	}
	defer os.RemoveAll(dir)

	in := filepath.Join(dir, "in."+outExt) // oxipng requires .png ext; cwebp sniffs
	out := filepath.Join(dir, "out."+outExt)
	if err := os.WriteFile(in, data, 0600); err != nil {
		return nil, fmt.Errorf("fileopt: write input: %w", err)
	}

	args := append([]string{}, extraArgs...)
	if io.InFirst {
		args = append(args, in, io.OutFlag, out)
	} else {
		args = append(args, io.OutFlag, out, in)
	}

	runCtx, cancel := context.WithTimeout(ctx, imageOptimizeTimeout)
	defer cancel()

	start := time.Now()
	//nolint:gosec // bin resolved above; args/paths under our control
	cmd := exec.CommandContext(runCtx, bin, args...)
	if stderr, err := cmd.CombinedOutput(); err != nil {
		RecordError(stage, time.Since(start))
		return nil, fmt.Errorf("fileopt: %s failed: %w (output: %s)", filepath.Base(bin), err, stderr)
	}

	got, err := os.ReadFile(out)
	if err != nil {
		RecordError(stage, time.Since(start))
		return nil, fmt.Errorf("fileopt: read output: %w", err)
	}
	// Bail-out: if the stage produced a LARGER output (e.g. cwebp on smooth
	// gradient — measured 40-70% bloat in bench), return the original. Still
	// record ratio so we can see the frequency in metrics.
	RecordSuccess(stage, len(data), len(got), time.Since(start))
	if len(got) >= len(data) {
		return data, nil
	}
	return got, nil
}
