package app

import (
	"context"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/ports"
)

func selectedOrganizationID(ctx context.Context, options ports.OrganizationSelection) (string, error) {
	organizations := options.Identity.Organizations
	if len(organizations) == 0 {
		name, err := options.Questions.Create(ctx)
		if err != nil {
			return "", err
		}
		return options.Create(ctx, name)
	}
	selected, err := options.Questions.Choose(ctx, organizations)
	if err != nil {
		return "", err
	}
	for _, organization := range organizations {
		if organization.ID == selected {
			return selected, nil
		}
	}
	return "", clierror.New("Choose an organization from your verified memberships.")
}

// SelectOrganization chooses one of the account's verified organizations (or creates the
// first) and refreshes the session into it (src/select-organization.ts).
func SelectOrganization(ctx context.Context, options ports.OrganizationSelection) (contracts.AccountSession, error) {
	if options.Identity.PrincipalID != options.Session.PrincipalID {
		return contracts.AccountSession{}, clierror.New("Account identity changed. Run durin to sign in again.")
	}
	selected, err := selectedOrganizationID(ctx, options)
	if err != nil {
		return contracts.AccountSession{}, err
	}
	session, err := options.Refresh(ctx, options.Session, selected)
	if err != nil {
		return contracts.AccountSession{}, err
	}
	if session.PrincipalID != options.Identity.PrincipalID || session.OrganizationID == nil || *session.OrganizationID != selected {
		return contracts.AccountSession{}, clierror.New("Account does not match the selected organization.")
	}
	return session, nil
}
