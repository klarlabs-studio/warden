package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// Predictions uses one atomic file per step; concurrent processes cannot lose
// unrelated entries. Racing writes cost a cache miss, never a false hit.
type Predictions struct {
	dir     string
	signer  application.Signer
	version string
	binary  string
}

func OpenPredictions(gitDir, version string, signer application.Signer) *Predictions {
	return &Predictions{dir: filepath.Join(gitDir, "warden", "predictions"), signer: signer, version: version, binary: predictionExecutableDigest()}
}
func (p *Predictions) path(step domain.StepName) string {
	key := domain.DigestBytes([]byte(string(step) + "\x00" + p.version + "\x00" + p.binary))
	return filepath.Join(p.dir, strings.TrimPrefix(key, "sha256:")+".json")
}
func (p *Predictions) Lookup(step domain.StepName, base string, contract domain.ExecutionContract) *domain.ExecutionReuse {
	if p.signer == nil || p.binary == "" {
		return nil
	}
	f, err := os.Open(p.path(step))
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil
	}
	var entry domain.Prediction
	if json.Unmarshal(raw, &entry) != nil {
		return nil
	}
	age := time.Since(entry.CreatedAt)
	if entry.BinaryDigest != p.binary || entry.WardenVersion != p.version || entry.Purpose != domain.PredictionPurpose || entry.Version != 1 || entry.Step != step || entry.BaseSHA != base || entry.PublicKey != p.signer.PublicKey() || entry.Algorithm != p.signer.Algorithm() || age < 0 || age > domain.PredictionTTL || !entry.VerifySignature() {
		return nil
	}
	key := domain.PredictionKey(step, base, contract)
	if key != domain.PredictionKey(entry.Step, entry.BaseSHA, entry.Contract) {
		return nil
	}
	return &domain.ExecutionReuse{Source: domain.PredictionPurpose, Key: key, RecordedAt: entry.CreatedAt}
}
func (p *Predictions) Record(step domain.StepName, base string, contract domain.ExecutionContract) (time.Time, bool) {
	if p.signer == nil || p.binary == "" {
		return time.Time{}, false
	}
	entry := domain.Prediction{BinaryDigest: p.binary, WardenVersion: p.version, Purpose: domain.PredictionPurpose, Version: 1, Step: step, BaseSHA: base, Contract: contract, CreatedAt: time.Now().UTC(), PublicKey: p.signer.PublicKey(), Algorithm: p.signer.Algorithm()}
	payload, err := entry.SigningPayload()
	if err != nil {
		return time.Time{}, false
	}
	entry.Signature, err = p.signer.Sign(payload)
	if err != nil {
		return time.Time{}, false
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return time.Time{}, false
	}
	if os.MkdirAll(p.dir, 0o700) != nil {
		return time.Time{}, false
	}
	f, err := os.CreateTemp(p.dir, "prediction-*")
	if err != nil {
		return time.Time{}, false
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return time.Time{}, false
	}
	if f.Close() != nil {
		return time.Time{}, false
	}
	if os.Rename(f.Name(), p.path(step)) != nil {
		return time.Time{}, false
	}
	return entry.CreatedAt, true
}

// A rebuilt development binary must not inherit results from a different
// implementation merely because both report the same version string.
var predictionExecutableDigest = sync.OnceValue(func() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
})
