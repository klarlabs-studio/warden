package domain

import (
	"encoding/json"
	"time"
)

// Prediction is local staged-tree evidence, never a commit attestation.
// The purpose discriminator prevents interpreting signatures as RunRecords.
type Prediction struct {
	BinaryDigest  string            `json:"binary_digest"`
	WardenVersion string            `json:"warden_version"`
	Purpose       string            `json:"purpose"`
	Version       int               `json:"version"`
	Step          StepName          `json:"step"`
	BaseSHA       string            `json:"base_sha"`
	Contract      ExecutionContract `json:"contract"`
	CreatedAt     time.Time         `json:"created_at"`
	PublicKey     string            `json:"public_key"`
	Algorithm     string            `json:"algorithm"`
	Signature     string            `json:"signature,omitempty"`
}

const PredictionPurpose = "warden-staged-prediction"
const PredictionTTL = 10 * time.Minute

func (p Prediction) SigningPayload() ([]byte, error) { p.Signature = ""; return json.Marshal(p) }
func (p Prediction) VerifySignature() bool {
	payload, err := p.SigningPayload()
	if err != nil {
		return false
	}
	switch p.Algorithm {
	case AlgorithmEd25519:
		return verifyEd25519Signature(p.PublicKey, p.Signature, payload)
	case AlgorithmSSH:
		return verifySSHSignature(p.PublicKey, p.Signature, payload)
	default:
		return false
	}
}

// PredictionKey binds the entire observed environment and tree, not path globs.
func PredictionKey(step StepName, base string, contract ExecutionContract) string {
	digest, _ := DigestJSON(struct {
		Version  int
		Step     StepName
		Base     string
		Contract ExecutionContract
	}{1, step, base, contract})
	return digest
}

type ExecutionReuse struct {
	Source     string    `json:"source"`
	Key        string    `json:"key"`
	RecordedAt time.Time `json:"recorded_at"`
}
