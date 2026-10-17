package steps

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/sbom"
)

// captureExecutionContract is conservative: only a clean tracked tree in a
// dependency-free container worktree has known initial inputs. The ordinary
// gate can still run if capture is unavailable; strict reuse cannot pass.
func captureExecutionContract(ctx context.Context, sc application.StepContext, command string) (*domain.ExecutionContract, error) {
	if sc.Container == nil || sc.Container.Environment.Network != "none" || !domain.ValidContainerImageID(sc.ConfigurationDigest) {
		return nil, nil
	}
	tree, inputs, err := contractSnapshotAt(ctx, sc.WorktreeDir, true, sc.Hook == domain.PreCommit)
	if err != nil {
		return nil, err
	}
	contextDigest, err := domain.DigestJSON(stepInputs(sc))
	if err != nil {
		return nil, err
	}
	return &domain.ExecutionContract{Version: domain.ExecutionContractVersion, CommandDigest: domain.DigestBytes([]byte(command)), ConfigurationDigest: sc.ConfigurationDigest, ContextDigest: contextDigest, TreeSHA: tree, Inputs: inputs, Environment: sc.Container.Environment}, nil
}

func contractSnapshot(ctx context.Context, dir string, requireEmpty bool) (checkedTree string, manifests map[string]string, captureErr error) {
	return contractSnapshotAt(ctx, dir, requireEmpty, false)
}

func contractSnapshotAt(ctx context.Context, dir string, requireEmpty, staged bool) (checkedTree string, manifests map[string]string, captureErr error) {
	// Git's clean status can hide assume-unchanged / skip-worktree files.
	flags, err := contractGit(ctx, dir, "ls-files", "-v", "-z")
	if err != nil {
		return "", nil, err
	}
	for _, entry := range strings.Split(string(flags), "\x00") {
		if entry != "" && (entry[0] == 'S' || entry[0] >= 'a' && entry[0] <= 'z') {
			return "", nil, fmt.Errorf("hidden index entries prevent complete input capture")
		}
	}
	entries, err := contractGit(ctx, dir, "ls-files", "--stage", "-z")
	if err != nil {
		return "", nil, err
	}
	for _, entry := range strings.Split(string(entries), "\x00") {
		if strings.HasPrefix(entry, "160000 ") {
			return "", nil, fmt.Errorf("submodule inputs are not supported by contract version 1")
		}
	}
	diffArgs := []string{"diff", "--quiet", "HEAD", "--"}
	if staged {
		diffArgs = []string{"diff", "--quiet", "--"}
	}
	if _, err := contractGit(ctx, dir, diffArgs...); err != nil {
		return "", nil, fmt.Errorf("tracked worktree is not clean")
	}
	if requireEmpty {
		others, err := contractGit(ctx, dir, "ls-files", "--others", "-z")
		if err != nil || len(others) > 0 {
			return "", nil, fmt.Errorf("untracked or ignored inputs prevent complete initial-input capture")
		}
	}
	treeArgs := []string{"rev-parse", "HEAD^{tree}"}
	if staged {
		treeArgs = []string{"write-tree"}
	}
	tree, err := contractGit(ctx, dir, treeArgs...)
	if err != nil || !domain.ValidTreeSHA(strings.TrimSpace(string(tree))) {
		return "", nil, fmt.Errorf("could not determine checked tree")
	}
	listed, err := contractGit(ctx, dir, "ls-files", "-z")
	if err != nil {
		return "", nil, err
	}
	inputs := make(map[string]string)
	for _, file := range strings.Split(string(listed), "\x00") {
		if file == "" || !sbom.IsDependencyManifest(filepath.Base(file)) {
			continue
		}
		target := filepath.Join(dir, filepath.FromSlash(file))
		info, err := os.Lstat(target)
		if err != nil || !info.Mode().IsRegular() {
			return "", nil, fmt.Errorf("dependency file is missing or not regular")
		}
		data, err := os.ReadFile(target)
		if err != nil {
			return "", nil, fmt.Errorf("dependency file could not be read")
		}
		inputs[file] = domain.DigestBytes(data)
	}
	return strings.TrimSpace(string(tree)), inputs, nil
}

func finishExecutionContract(ctx context.Context, sc application.StepContext, contract *domain.ExecutionContract) *domain.ExecutionContract {
	if contract == nil {
		return nil
	}
	tree, inputs, err := contractSnapshotAt(ctx, sc.WorktreeDir, false, sc.Hook == domain.PreCommit)
	if err != nil || tree != contract.TreeSHA || !maps.Equal(inputs, contract.Inputs) {
		if sc.OnOutput != nil {
			sc.OnOutput("warden: execution contract unavailable because tracked inputs changed during the check")
		}
		return nil
	}
	return contract
}

func contractGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = scrubbedEnv()
	return cmd.Output()
}
