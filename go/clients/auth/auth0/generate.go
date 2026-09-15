package auth0

// Mock generation for the Auth0 SDK-adapter seams. The seams are EXPORTED
// (OrgPager/MemberPager/OrgWriter/Inviter/MemberRoleLister) so their mocks live in
// the external pkg/go/tests/mocks tree — never in-package — and a black-box test
// (pkg/go/tests/clients) drives the pagination / nil-skip / role-fan-out behavior
// through the exported NewWithSeams constructor.

//go:generate mockgen -destination=../../../tests/mocks/mock_auth0_seams.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/clients/auth/auth0 OrgPager,MemberPager,OrgWriter,Inviter,MemberRoleLister,UserProvisioner
