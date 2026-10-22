package repair18

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

func ProposalIdentity(p Proposer) string {
	var value string
	if identity, ok := p.(interface{ PolicyIdentity() string }); ok {
		value = identity.PolicyIdentity()
	} else {
		value = fmt.Sprintf("trusted-injected:%T:protocol-v2", p)
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func (d DeepSeek) PolicyIdentity() string {
	model := d.Model
	if model == "" {
		model = "deepseek-flash"
	}
	endpoint := d.Endpoint
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	return "deepseek-proposal-v2:" + endpoint + ":" + model + ":high:8192"
}
