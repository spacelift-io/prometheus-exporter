package session

type user struct {
	JWT        string `graphql:"jwt"` //nolint:gosec // this is not a stored secret
	ValidUntil int64  `graphql:"validUntil"`
}
