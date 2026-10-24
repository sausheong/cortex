package keys

import "crypto/subtle"

// Equal reports whether two keys hold the same bytes, in constant time. A
// zeroed or nil key equals nothing.
func (u *UK) Equal(o *UK) bool {
	if u == nil || o == nil {
		return false
	}
	a, b := u.Bytes(), o.Bytes()
	defer clear(a)
	defer clear(b)
	return a != nil && b != nil && subtle.ConstantTimeCompare(a, b) == 1
}
