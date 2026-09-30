package crm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Demo seed values.
const (
	DemoProfileName  = "Default Assistant"
	DemoSIPNumber    = "+97670001234"
	DemoGreeting     = "Сайн байна уу, CallGo-ийн туслах байна. Танд юугаар туслах вэ?"
	DemoSystemPrompt = `Та бол CallGo.mn дуудлагын төвийн эелдэг, туслах сэтгэлтэй виртуал туслах юм.
Дүрэм:
- Үргэлж монгол хэлээр, хүндэтгэлтэй "Та" гэж харьцаж ярь.
- Хариултаа богино, ойлгомжтой, нэг удаад 1-2 өгүүлбэрээр өг; утсаар ярьж байгаа тул жагсаалт, тэмдэгт бүү ашигла.
- Харилцагчийн хэрэгцээг тодруулах асуулт асууж, мэдээллийг нь баталгаажуулж давт.
- Мэдэхгүй зүйлээ зохиож бүү хэл; шаардлагатай бол ажилтанд шилжүүлэхийг санал болго.
- Утасны дугаар, тоо, огноог тод, удаан хэл.
- Яриа дуусахад талархал илэрхийлж, эелдэгээр салах ёс гүйцэтгэ.`
	demoAdminName = "Admin"
	demoSeedLock  = "callgo.crm.seed_demo"
	demoTrialDays = 14
)

// SeedDemo idempotently ensures the demo tenant: the "demo" organisation (plan
// trial) with a 14-day trialing subscription, an owner user (adminEmail /
// passwordHash, active, e-mail verified, platform admin; an existing user is
// left untouched),
// a "Default Assistant" agent profile and the demo SIP number +97670001234
// bound to it. Concurrent callers are serialised with an advisory lock.
func (s *Store) SeedDemo(ctx context.Context, adminEmail, passwordHash string) (*domain.Organization, error) {
	adminEmail = strings.TrimSpace(adminEmail)
	if adminEmail == "" {
		return nil, fmt.Errorf("crm: seed demo: admin email: %w", domain.ErrInvalid)
	}
	var org *domain.Organization
	err := s.inTx(ctx, func(tx *Store) error {
		if _, err := tx.db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, demoSeedLock); err != nil {
			return dbErr("seed demo: lock", err)
		}
		o, err := tx.EnsureDefaultOrg(ctx)
		if err != nil {
			return err
		}
		org = o

		if _, err := tx.GetUserByEmail(ctx, adminEmail); errors.Is(err, domain.ErrNotFound) {
			verified := time.Now()
			u := &domain.User{OrgID: o.ID, Email: adminEmail, Name: demoAdminName, Role: domain.RoleOwner,
				PasswordHash: passwordHash, Status: domain.UserActive, EmailVerifiedAt: &verified,
				IsPlatformAdmin: true}
			if err := tx.CreateUser(ctx, u); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}

		if err := tx.ensureDemoSubscription(ctx, o); err != nil {
			return err
		}

		profile, err := tx.demoProfile(ctx, o)
		if err != nil {
			return err
		}

		n, err := tx.GetSIPNumberByNumber(ctx, DemoSIPNumber)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			return tx.CreateSIPNumber(ctx, &domain.SIPNumber{
				OrgID: o.ID, Number: DemoSIPNumber, Label: "Demo line", AgentProfileID: &profile.ID,
				AllowInbound: true, AllowOutbound: true, Active: true,
			})
		case err != nil:
			return err
		case n.OrgID == o.ID && n.AgentProfileID == nil:
			n.AgentProfileID = &profile.ID
			return tx.UpdateSIPNumber(ctx, n)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return org, nil
}

// ensureDemoSubscription gives the demo org a 14-day trialing subscription
// on its plan (trial) unless it already has one.
func (s *Store) ensureDemoSubscription(ctx context.Context, o *domain.Organization) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO subscriptions (org_id, plan_code, status, current_period_start, current_period_end, trial_ends_at)
		 VALUES ($1, $2, $3, now(), now() + $4 * interval '1 day', now() + $4 * interval '1 day')
		 ON CONFLICT (org_id) DO NOTHING`,
		o.ID, o.PlanCode, string(domain.SubTrialing), demoTrialDays)
	return dbErr("seed demo: subscription", err)
}

func (s *Store) demoProfile(ctx context.Context, o *domain.Organization) (*domain.AgentProfile, error) {
	profiles, err := s.ListAgentProfiles(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	for i := range profiles {
		if profiles[i].Name == DemoProfileName {
			return &profiles[i], nil
		}
	}
	p := &domain.AgentProfile{
		OrgID:          o.ID,
		Name:           DemoProfileName,
		SystemPrompt:   DemoSystemPrompt,
		Greeting:       DemoGreeting,
		Language:       DefaultLanguage,
		STTProvider:    "faster_whisper",
		STTModel:       "large-v3",
		TTSProvider:    "piper",
		TTSVoice:       "mn_MN-default-medium",
		MaxDurationSec: 600,
		Tools:          []string{"end_call", "transfer_call", "lookup_contact", "schedule_callback"},
	}
	if err := s.CreateAgentProfile(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}
