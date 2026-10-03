package accounts

import "context"

// DeletePending removes a user who is still pending, with its account,
// sessions and codes. It is signup's rollback when the verification code
// could not be sent (spec/accounts.md, 503 email_unavailable): leaving the
// row would hold the address for 7 days with no code anyone received. A user
// who verified meanwhile is left alone. Audit rows stay.
func (s *Service) DeletePending(ctx context.Context, userID int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM accounts a USING users u
		WHERE u.account_id = a.id AND u.id = $1 AND u.status = 'pending'`, userID)
	return err
}
