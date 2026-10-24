package keys

import (
	"errors"
)

// WrapKeyPasskey derives a wrap key from a WebAuthn PRF output.
func WrapKeyPasskey(prf []byte) []byte { return hkdfKey(prf, "cortex/wrap/passkey/v1") }

// WrapKeyRecovery derives a wrap key from raw recovery key bytes.
func WrapKeyRecovery(raw []byte) []byte { return hkdfKey(raw, "cortex/wrap/recovery/v1") }

// WrapKeySecret derives a wrap key from a bearer secret (session secret,
// agent token, oauth code or token). kind selects the domain.
func WrapKeySecret(kind, secret string) []byte {
	return hkdfKey([]byte(secret), "cortex/wrap/"+kind+"/v1")
}

// Wrap encrypts the UK under wrapKey, bound to (userID, kind, refID).
func Wrap(wrapKey []byte, userID, kind, refID string, uk *UK) ([]byte, error) {
	if len(wrapKey) != 32 {
		return nil, errors.New("keys: wrap key must be 32 bytes")
	}
	raw := uk.Bytes()
	if raw == nil {
		return nil, errors.New("keys: key zeroed")
	}
	defer clear(raw)
	return seal(wrapKey, aad("cortex/wrap/v1", userID, kind, refID), raw)
}

// Unwrap recovers a UK; any mismatch yields ErrDecrypt.
func Unwrap(wrapKey []byte, userID, kind, refID string, wrapped []byte) (*UK, error) {
	if len(wrapKey) != 32 {
		return nil, ErrDecrypt
	}
	raw, err := open(wrapKey, aad("cortex/wrap/v1", userID, kind, refID), wrapped)
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	uk, err := FromBytes(raw)
	if err != nil {
		return nil, ErrDecrypt
	}
	return uk, nil
}
