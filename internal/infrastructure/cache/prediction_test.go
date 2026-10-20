package cache

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/domain"
)

type predictionSigner struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

func (s predictionSigner) PublicKey() string { return base64.StdEncoding.EncodeToString(s.pub) }
func (s predictionSigner) Algorithm() string { return domain.AlgorithmEd25519 }
func (s predictionSigner) Sign(data []byte) (string, error) {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(s.priv, data)), nil
}
func TestPredictionsExactSignedReuse(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer := predictionSigner{pub, priv}
	store := OpenPredictions(t.TempDir(), "test-version", signer)
	contract := domain.ExecutionContract{Version: 1, TreeSHA: strings.Repeat("a", 40), CommandDigest: domain.DigestBytes([]byte("test")), Inputs: map[string]string{}}
	base := strings.Repeat("b", 40)
	savedAt, ok := store.Record(domain.StepTest, base, contract)
	reuse := store.Lookup(domain.StepTest, base, contract)
	if !ok || reuse == nil {
		t.Fatal("lost valid signed prediction")
	}
	// The reported time is the recorded one, so an expiry derived from it is
	// the prediction's own.
	if !reuse.RecordedAt.Equal(savedAt) {
		t.Fatalf("Record reported %v, the prediction says %v", savedAt, reuse.RecordedAt)
	}
	for _, tt := range []struct {
		name   string
		change func(*domain.ExecutionContract)
	}{
		{"configuration", func(c *domain.ExecutionContract) { c.ConfigurationDigest = "changed" }},
		{"context", func(c *domain.ExecutionContract) { c.ContextDigest = "changed" }},
		{"tree", func(c *domain.ExecutionContract) { c.TreeSHA = strings.Repeat("c", 40) }},
		{"image", func(c *domain.ExecutionContract) { c.Environment.ImageID = "changed" }},
		{"runtime", func(c *domain.ExecutionContract) { c.Environment.Runtime = "changed" }},
		{"inputs", func(c *domain.ExecutionContract) { c.Inputs = map[string]string{"go.mod": "changed"} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			changed := contract
			tt.change(&changed)
			if store.Lookup(domain.StepTest, base, changed) != nil {
				t.Fatal("accepted different input scope")
			}
		})
	}
	changed := contract
	changed.CommandDigest = "changed"
	if store.Lookup(domain.StepTest, base, changed) != nil {
		t.Fatal("accepted different command")
	}
	if store.Lookup(domain.StepTest, strings.Repeat("c", 40), contract) != nil {
		t.Fatal("accepted different base")
	}
	data, err := os.ReadFile(store.path(domain.StepTest))
	if err != nil {
		t.Fatal(err)
	}
	var original domain.Prediction
	if err := json.Unmarshal(data, &original); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		change func(*domain.Prediction)
		resign bool
	}{
		{"expired", func(p *domain.Prediction) { p.CreatedAt = time.Now().Add(-11 * time.Minute) }, true},
		{"future", func(p *domain.Prediction) { p.CreatedAt = time.Now().Add(time.Minute) }, true},
		{"purpose", func(p *domain.Prediction) { p.Purpose = "other" }, true},
		{"version", func(p *domain.Prediction) { p.WardenVersion = "other" }, true},
		{"tamper", func(p *domain.Prediction) { p.CreatedAt = p.CreatedAt.Add(time.Second) }, false},
		{"unknown key", func(p *domain.Prediction) { p.PublicKey = "other" }, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			entry := original
			tt.change(&entry)
			if tt.resign {
				payload, _ := entry.SigningPayload()
				entry.Signature, _ = signer.Sign(payload)
			}
			data, _ := json.Marshal(entry)
			if err := os.WriteFile(store.path(domain.StepTest), data, 0600); err != nil {
				t.Fatal(err)
			}
			if store.Lookup(domain.StepTest, base, contract) != nil {
				t.Fatal("accepted invalid prediction")
			}
		})
	}
	if OpenPredictions(t.TempDir(), "test", nil).Lookup(domain.StepTest, base, contract) != nil {
		t.Fatal("unsigned reuse")
	}
}
