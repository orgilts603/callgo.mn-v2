package crm

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func TestContacts(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "acme")
	other := newOrg(t, ctx, s, "other")

	c := &domain.Contact{OrgID: org.ID, Phone: " +97699112233 ", Name: "Бат", Tags: []string{"vip", "vip", " "},
		Meta: map[string]string{"city": "УБ", "tier": "gold"}}
	require.NoError(t, s.UpsertContact(ctx, c))
	id := c.ID
	require.NotEqual(t, uuid.Nil, id)
	require.Equal(t, "+97699112233", c.Phone)
	require.Equal(t, []string{"vip"}, c.Tags)
	require.False(t, c.CreatedAt.IsZero())

	// Merge: empty name keeps old, tags union in order, meta merged.
	m := &domain.Contact{OrgID: org.ID, Phone: "+97699112233", Tags: []string{"new", "vip"},
		Meta: map[string]string{"tier": "platinum", "lang": "mn"}}
	require.NoError(t, s.UpsertContact(ctx, m))
	require.Equal(t, id, m.ID)
	require.Equal(t, "Бат", m.Name)
	require.Equal(t, []string{"vip", "new"}, m.Tags)
	require.Equal(t, map[string]string{"city": "УБ", "tier": "platinum", "lang": "mn"}, m.Meta)
	require.True(t, !m.UpdatedAt.Before(c.UpdatedAt))

	m2 := &domain.Contact{OrgID: org.ID, Phone: "+97699112233", Name: "Бат-Эрдэнэ"}
	require.NoError(t, s.UpsertContact(ctx, m2))
	require.Equal(t, "Бат-Эрдэнэ", m2.Name)
	require.Equal(t, []string{"vip", "new"}, m2.Tags)

	// Same phone in another org is a different contact.
	o := &domain.Contact{OrgID: other.ID, Phone: "+97699112233"}
	require.NoError(t, s.UpsertContact(ctx, o))
	require.NotEqual(t, id, o.ID)
	require.Equal(t, []string{}, o.Tags)
	require.Equal(t, map[string]string{}, o.Meta)

	got, err := s.GetContact(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "Бат-Эрдэнэ", got.Name)
	got, err = s.GetContactByPhone(ctx, org.ID, "+97699112233")
	require.NoError(t, err)
	require.Equal(t, id, got.ID)
	_, err = s.GetContactByPhone(ctx, org.ID, "+1")
	requireErrIs(t, err, domain.ErrNotFound)
	_, err = s.GetContact(ctx, uuid.New())
	requireErrIs(t, err, domain.ErrNotFound)

	for i := range 5 {
		require.NoError(t, s.UpsertContact(ctx, &domain.Contact{OrgID: org.ID,
			Phone: fmt.Sprintf("+9768800000%d", i), Name: fmt.Sprintf("Customer %d", i)}))
	}
	list, total, err := s.ListContacts(ctx, org.ID, "", 0, 0)
	require.NoError(t, err)
	require.Equal(t, 6, total)
	require.Len(t, list, 6)
	require.Equal(t, "+97688000004", list[0].Phone, "most recently updated first")

	list, total, err = s.ListContacts(ctx, org.ID, "customer", 2, 1)
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Len(t, list, 2)
	list, total, err = s.ListContacts(ctx, org.ID, "эрдэнэ", 10, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, id, list[0].ID)
	list, total, err = s.ListContacts(ctx, org.ID, "88000003", 10, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, "Customer 3", list[0].Name)
	list, total, err = s.ListContacts(ctx, org.ID, "x", 10, 50)
	require.NoError(t, err)
	require.Equal(t, 0, total)
	require.NotNil(t, list)

	require.NoError(t, s.DeleteContact(ctx, id))
	requireErrIs(t, s.DeleteContact(ctx, id), domain.ErrNotFound)
}
