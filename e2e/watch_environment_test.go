package e2e

import (
	"archive/tar"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A retagged image is a different environment. A staged prediction is bound to
// the image ID its check ran under, so after `docker build -t team-checks:local`
// (or any retag of the configured name) a pass the watcher reported earlier no
// longer describes what a commit would run. The watcher used to fingerprint
// only the index and the configuration: it never re-evaluated, and its last
// line kept saying "staged checks passed" for an image no longer in use.
//
// The test builds the retag itself, offline, and reads both image IDs from the
// engine directly, so the expected identities exist independently of warden.
func TestGoldenFleet_RetaggedImageReEvaluatesTheStagedWatch(t *testing.T) {
	image := os.Getenv("WARDEN_CONTAINER_TEST_IMAGE")
	if image == "" {
		t.Skip("set WARDEN_CONTAINER_TEST_IMAGE to a preloaded Linux image")
	}
	engine := os.Getenv("WARDEN_CONTAINER_TEST_RUNTIME")
	if engine == "" {
		engine = "docker"
	}
	cli := engine
	if engine == "apple" {
		cli = "container"
	}
	nonce := randomHex(t)
	tag := "warden-e2e-retag-" + nonce + ":local"
	engineRun(t, cli, "image", "tag", image, tag)
	t.Cleanup(func() { _ = exec.Command(cli, "image", "rm", tag).Run() })
	oldID := engineImageID(t, engine, cli, tag)

	g := newGoldenRepo(t)
	keyDir := t.TempDir()
	cfg := fmt.Sprintf(`hooks: { pre_commit: true }
execution: { image: %q, runtime: %q }
predictive_cache: [test]
steps: { pre_commit: [test], pre_push: [test] }
commands: { test: 'test "$(cat sample)" = good' }
rules: []
notify: false
`, tag, engine)
	if err := os.WriteFile(filepath.Join(g.dir, ".warden.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(g.dir, "sample"), []byte("initial"), 0o600); err != nil {
		t.Fatal(err)
	}
	g.git("add", ".warden.yaml", "sample")
	g.git("commit", "--no-verify", "-m", "configure staged checks")
	if err := os.WriteFile(filepath.Join(g.dir, "sample"), []byte("good"), 0o600); err != nil {
		t.Fatal(err)
	}
	g.git("add", "sample")

	out, stop := startWatcher(t, g.dir, keyDir)
	ranUnder := func(id string) string {
		return fmt.Sprintf("    in runtime %s, image %s (%s)", engine, tag, id)
	}
	waitForOutput(t, out, "the first staged evaluation", func(s string) bool {
		return strings.Contains(s, "staged checks passed")
	})
	if !strings.Contains(out.String(), ranUnder(oldID)) {
		t.Fatalf("first evaluation did not report the image it ran under (%s):\n%s", oldID, out.String())
	}
	predictions := filepath.Join(g.dir, ".git", "warden", "predictions")
	oldPrediction := onlyPrediction(t, predictions)

	retag(t, engine, cli, tag, nonce)
	newID := engineImageID(t, engine, cli, tag)
	if newID == oldID {
		t.Fatalf("the retag did not change the image ID (%s)", newID)
	}
	waitForOutput(t, out, "the evaluation under the retagged image", func(s string) bool {
		return strings.Contains(s, ranUnder(newID)) && strings.Count(s, "staged checks passed") >= 2
	})
	if !strings.Contains(out.String(), "warden: environment changed: runtime "+engine+", image "+tag+" ("+newID+")") {
		t.Fatalf("the retag was not reported:\n%s", out.String())
	}
	if code := stop(); code != 0 {
		t.Fatalf("watcher exit %d:\n%s", code, out.String())
	}
	newPrediction := onlyPrediction(t, predictions)

	warden := func(args ...string) (string, int) {
		return g.wardenEnv([]string{"WARDEN_CONFIG_DIR=" + keyDir}, args...)
	}
	// The prediction for the previous image must not authorize the new one,
	// although the staged tree, the configuration and the signer are the same.
	writePrediction(t, predictions, oldPrediction)
	if got, code := warden("run", "pre-commit"); code != 0 || strings.Contains(got, "matched signed staged prediction") {
		t.Fatalf("the previous image's prediction was reused: %d %s", code, got)
	}
	// The watcher's re-evaluation is what the gate reuses.
	writePrediction(t, predictions, newPrediction)
	if got, code := warden("run", "pre-commit"); code != 0 || !strings.Contains(got, "matched signed staged prediction") {
		t.Fatalf("the re-evaluated prediction was not reused: %d %s", code, got)
	}
}

func randomHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func engineRun(t *testing.T, cli string, args ...string) []byte {
	t.Helper()
	out, err := exec.Command(cli, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", cli, args, err, out)
	}
	return out
}

// engineImageID reads the identity warden should bind straight from the
// engine: Apple's linux/arm64 platform manifest digest, or the config ID the
// Docker-compatible engines report (Podman prints it without the algorithm).
func engineImageID(t *testing.T, engine, cli, ref string) string {
	t.Helper()
	data, err := exec.Command(cli, "image", "inspect", ref).Output()
	if err != nil {
		t.Fatalf("inspect %s: %v", ref, err)
	}
	if engine == "apple" {
		var images []struct {
			Variants []struct {
				Digest   string `json:"digest"`
				Platform struct {
					OS           string `json:"os"`
					Architecture string `json:"architecture"`
				} `json:"platform"`
			} `json:"variants"`
		}
		if err := json.Unmarshal(data, &images); err != nil || len(images) != 1 {
			t.Fatalf("apple inspection: %v %s", err, data)
		}
		for _, v := range images[0].Variants {
			if v.Platform.OS == "linux" && v.Platform.Architecture == "arm64" {
				return v.Digest
			}
		}
		t.Fatalf("no linux/arm64 variant: %s", data)
	}
	var images []struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(data, &images); err != nil || len(images) != 1 {
		t.Fatalf("inspection: %v %s", err, data)
	}
	if !strings.HasPrefix(images[0].ID, "sha256:") {
		return "sha256:" + images[0].ID
	}
	return images[0].ID
}

// retag points tag at a new image derived from the one it names, without the
// network: the Docker-compatible engines commit a stopped container with a
// changed CMD (the one --change directive all three accept); Apple has no
// commit, so its saved OCI layout is rewritten with a changed config and
// loaded back under the same name.
func retag(t *testing.T, engine, cli, tag, nonce string) {
	t.Helper()
	if engine == "apple" {
		appleRetag(t, tag, nonce)
		return
	}
	name := "warden-e2e-retag-" + nonce
	engineRun(t, cli, "create", "--name", name, tag, "true")
	defer func() { _ = exec.Command(cli, "rm", "-f", name).Run() }()
	engineRun(t, cli, "commit", "--change", `CMD ["/bin/sh","-c","echo `+nonce+`"]`, name, tag)
}

func appleRetag(t *testing.T, tag, nonce string) {
	t.Helper()
	dir := t.TempDir()
	archive := filepath.Join(dir, "image.tar")
	engineRun(t, "container", "image", "save", "--platform", "linux/arm64", "-o", archive, tag)
	files := readTar(t, archive)
	var index struct {
		Manifests []map[string]any `json:"manifests"`
	}
	if err := json.Unmarshal(files["index.json"], &index); err != nil {
		t.Fatalf("layout index: %v", err)
	}
	var top map[string]any
	_ = json.Unmarshal(files["index.json"], &top)
	for i, desc := range index.Manifests {
		index.Manifests[i] = rewriteDescriptor(t, files, desc, nonce)
	}
	top["manifests"] = index.Manifests
	files["index.json"] = mustJSON(t, top)
	rewritten := filepath.Join(dir, "retagged.tar")
	writeTar(t, rewritten, files)
	engineRun(t, "container", "image", "load", "-i", rewritten)
}

// rewriteDescriptor changes the image config under desc (adding a label) and
// re-addresses every manifest and index above it. Content the layout does not
// hold (other platforms' manifests) is left as it is.
func rewriteDescriptor(t *testing.T, files map[string][]byte, desc map[string]any, nonce string) map[string]any {
	t.Helper()
	digest, _ := desc["digest"].(string)
	path := "blobs/sha256/" + strings.TrimPrefix(digest, "sha256:")
	blob, ok := files[path]
	if !ok {
		return desc
	}
	var doc map[string]any
	if err := json.Unmarshal(blob, &doc); err != nil {
		t.Fatalf("blob %s: %v", digest, err)
	}
	mediaType, _ := desc["mediaType"].(string)
	switch {
	case strings.Contains(mediaType, "index") || strings.Contains(mediaType, "manifest.list"):
		children, _ := doc["manifests"].([]any)
		for i, child := range children {
			if m, ok := child.(map[string]any); ok {
				children[i] = rewriteDescriptor(t, files, m, nonce)
			}
		}
	case strings.Contains(mediaType, "manifest"):
		cfg, _ := doc["config"].(map[string]any)
		cfgPath := "blobs/sha256/" + strings.TrimPrefix(cfg["digest"].(string), "sha256:")
		var config map[string]any
		if err := json.Unmarshal(files[cfgPath], &config); err != nil {
			t.Fatalf("image config: %v", err)
		}
		inner, _ := config["config"].(map[string]any)
		if inner == nil {
			inner = map[string]any{}
		}
		inner["Labels"] = map[string]any{"dev.warden.e2e.retag": nonce}
		config["config"] = inner
		doc["config"] = storeBlob(t, files, cfg, mustJSON(t, config))
	default:
		t.Fatalf("unexpected media type %q", mediaType)
	}
	return storeBlob(t, files, desc, mustJSON(t, doc))
}

func storeBlob(t *testing.T, files map[string][]byte, desc map[string]any, data []byte) map[string]any {
	t.Helper()
	sum := sha256.Sum256(data)
	files["blobs/sha256/"+hex.EncodeToString(sum[:])] = data
	out := map[string]any{}
	for k, v := range desc {
		out[k] = v
	}
	out["digest"] = "sha256:" + hex.EncodeToString(sum[:])
	out["size"] = len(data)
	return out
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func readTar(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	files := map[string][]byte{}
	r := tar.NewReader(f)
	for {
		h, err := r.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		data, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		files[strings.TrimPrefix(h.Name, "./")] = data
	}
}

func writeTar(t *testing.T, path string, files map[string][]byte) {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for name, data := range files {
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func onlyPrediction(t *testing.T, dir string) []byte {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("prediction artifacts: %v %v", entries, err)
	}
	data, err := os.ReadFile(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writePrediction(t *testing.T, dir string, data []byte) {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("prediction artifacts: %v %v", entries, err)
	}
	if err := os.WriteFile(entries[0], data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// syncBuffer collects a background process's output for polling.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// startWatcher runs `warden watch --staged` in the background. stop interrupts
// it as ctrl+c would and returns its exit code.
func startWatcher(t *testing.T, dir, keyDir string) (*syncBuffer, func() int) {
	t.Helper()
	cmd := exec.Command(wardenBin, "watch", "--staged")
	cmd.Dir = dir
	cmd.Env = append(hookEnv(), "WARDEN_CONFIG_DIR="+keyDir)
	out := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	code := -1
	stop := func() int {
		if stopped {
			return code
		}
		stopped = true
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			code = 0
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			}
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
			t.Errorf("watcher did not stop:\n%s", out.String())
		}
		return code
	}
	t.Cleanup(func() { stop() })
	return out, stop
}

// waitForOutput allows for a cold engine and the 15 s environment sample.
func waitForOutput(t *testing.T, out *syncBuffer, what string, cond func(string) bool) {
	t.Helper()
	deadline := time.Now().Add(150 * time.Second)
	for !cond(out.String()) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s:\n%s", what, out.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
}
