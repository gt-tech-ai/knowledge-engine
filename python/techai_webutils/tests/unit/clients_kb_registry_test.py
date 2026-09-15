"""Tests for the tenant→KB registry client (pure in-memory backends) — audit F5.

The registry resolves an isolation unit (keyed by ``workspace_id``) to its Bedrock KB coordinates.
 ships two config-selected backends — ``shared`` (one KB for every key, the
behavior-preserving default) and ``output`` (a per-key map loaded from the Terraform ``for_each``
output at deploy) — behind a ``kb_registry_from_config`` factory. Both are pure map lookups (no I/O),
so these tests construct real backends over fixed in-test data — no mocks, no fakes.
"""

from __future__ import annotations

import pytest

from techai_webutils.core.interfaces.kb_registry import (
    ACTIVE_STATUS,
    MIGRATING_STATUS,
    PROVISIONING_STATUS,
    KbBinding,
    KbCoordinates,
)


def _coords(kb: str = "kb-shared") -> KbCoordinates:
    """A KbCoordinates fixture — the three-field Bedrock coordinate the registry resolves to.

    ``s3_prefix`` is the org's single Bedrock inclusion prefix (``{org_external_id}/``) — one prefix per
    org, the only per-org scoping Bedrock allows (`inclusionPrefixes` is capped at 1 item).
    """
    return KbCoordinates(knowledge_base_id=kb, data_source_id=f"ds-{kb}", s3_prefix=f"{kb}/")


class TestSharedKbRegistry:
    """The ``shared`` backend: one configured KB returned for every key (behavior-preserving default)."""

    @pytest.mark.asyncio
    async def test_resolves_any_workspace_to_the_configured_kb(self) -> None:
        """Test that the shared registry returns the one configured KB for any workspace id.

        **Why this test is important:**
          - ``shared`` is the default topology (design §6): until per-org KBs are provisioned +
            migrated, every workspace must resolve to the single global KB — a regression here would
            silently break retrieval for every tenant.

        **What it tests:**
          - ``resolve(<any workspace id>)`` returns the configured ``KbCoordinates`` regardless of key.
        """
        from techai_webutils.clients.kb_registry.shared.registry import SharedKbRegistry

        registry = SharedKbRegistry(_coords())

        assert await registry.resolve("workspace-a") == _coords()
        assert await registry.resolve("workspace-b") == _coords()

    @pytest.mark.asyncio
    async def test_list_active_returns_the_single_binding(self) -> None:
        """Test that the shared registry advertises exactly one active binding for write-path fan-out.

        **Why this test is important:**
          - The ingestion write path fans out one lock-guarded runner per ``list_active()`` binding
            (design §2); the shared topology must yield exactly one so the worker keeps its single
            global KB-sync loop (no accidental fan-out, no missing loop).

        **What it tests:**
          - ``list_active()`` returns a single ``active`` binding carrying the configured coordinates.
        """
        from techai_webutils.clients.kb_registry.shared.registry import SharedKbRegistry

        bindings = await SharedKbRegistry(_coords()).list_active()

        assert len(bindings) == 1
        assert bindings[0].coordinates == _coords()
        assert bindings[0].status == ACTIVE_STATUS


class TestOutputBackedKbRegistry:
    """The ``output`` backend: an ``org_external_id``-keyed ``KbBinding`` map (from the Terraform output)."""

    def _bindings(self) -> dict[str, KbBinding]:
        """Three org bindings (keyed by ``org_external_id``): two active, one provisioning."""
        return {
            "org_acme": KbBinding("org_acme", _coords("kb-a"), ACTIVE_STATUS),
            "org_globex": KbBinding("org_globex", _coords("kb-b"), ACTIVE_STATUS),
            "org_initech": KbBinding("org_initech", _coords("kb-c"), PROVISIONING_STATUS),
        }

    @pytest.mark.asyncio
    async def test_resolves_key_to_its_active_binding_coordinates(self) -> None:
        """Test that a known org with an active binding resolves to its own KB coordinates.

        **Why this test is important:**
          - Per-org isolation means an org must reach ITS KB, not the shared one; a mis-resolution is
            cross-tenant leakage (the security invariant, design "hard constraints").

        **What it tests:**
          - ``resolve("org_acme")`` returns ``kb-a``'s coordinates (its own binding), not another's.
        """
        from techai_webutils.clients.kb_registry.output.registry import OutputBackedKbRegistry

        registry = OutputBackedKbRegistry(self._bindings())

        assert await registry.resolve("org_acme") == _coords("kb-a")
        assert await registry.resolve("org_globex") == _coords("kb-b")

    @pytest.mark.asyncio
    async def test_unknown_key_fails_closed(self) -> None:
        """Test that an unknown org resolves to ``None`` (fails closed — never another org's KB).

        **Why this test is important:**
          - Fail-closed is THE security invariant: an unmapped org must return nothing, never fall
            through to a default/other-tenant KB (design §3).

        **What it tests:**
          - ``resolve("org_unknown")`` returns ``None``.
        """
        from techai_webutils.clients.kb_registry.output.registry import OutputBackedKbRegistry

        registry = OutputBackedKbRegistry(self._bindings())

        assert await registry.resolve("org_unknown") is None

    @pytest.mark.asyncio
    async def test_non_active_binding_fails_closed(self) -> None:
        """Test that a provisioning/migrating binding resolves to ``None`` (routes only when active).

        **Why this test is important:**
          - Reads/writes route to a tenant KB ONLY at ``active`` (design §5 FSM); resolving a
            still-provisioning or mid-migration binding would read an incomplete index.

        **What it tests:**
          - ``resolve("org_initech")`` (status ``provisioning``) returns ``None``.
        """
        from techai_webutils.clients.kb_registry.output.registry import OutputBackedKbRegistry

        registry = OutputBackedKbRegistry(self._bindings())

        assert await registry.resolve("org_initech") is None

    @pytest.mark.asyncio
    async def test_list_active_filters_out_non_active_bindings(self) -> None:
        """Test that ``list_active`` returns only ``active`` bindings (not provisioning/migrating).

        **Why this test is important:**
          - The write path must fan out a KB-sync runner only for a KB that is actually live; fanning
            out to a still-provisioning KB would start ingestion before the index exists.

        **What it tests:**
          - ``list_active()`` returns the two active bindings and excludes the provisioning one.
        """
        from techai_webutils.clients.kb_registry.output.registry import OutputBackedKbRegistry

        active = await OutputBackedKbRegistry(self._bindings()).list_active()

        assert {b.tenant_key for b in active} == {"org_acme", "org_globex"}
        assert all(b.status == ACTIVE_STATUS for b in active)

    @pytest.mark.asyncio
    async def test_migrating_binding_is_not_active(self) -> None:
        """Test that a ``migrating`` binding is excluded from ``list_active`` and fails closed on read.

        **Why this test is important:**
          - During migration (design §5) the tenant still reads the shared KB; the tenant KB must not
            appear active on either the read or the write path until cutover flips it to ``active``.

        **What it tests:**
          - A ``migrating`` binding is absent from ``list_active()`` and ``resolve`` yields ``None``.
        """
        from techai_webutils.clients.kb_registry.output.registry import OutputBackedKbRegistry

        registry = OutputBackedKbRegistry(
            {"org_migrating": KbBinding("org_migrating", _coords("kb-m"), MIGRATING_STATUS)}
        )

        assert await registry.resolve("org_migrating") is None
        assert await registry.list_active() == []

    @pytest.mark.asyncio
    async def test_org_external_id_key_space_round_trips(self) -> None:
        """Test that the read key (an Auth0 org_external_id) equals the population key (the TF output map).

        **Why this test is important:**
          - Per-org routing hinges on ONE key-space: the Terraform ``per_org_kbs`` map key, the module's
            ``per_org_knowledge_bases`` output, the registry map, and ``QueryStreamRequest.org_id`` must be
            the same ``org_external_id`` string. A mismatch (Auth0 org id vs internal UUID vs workspace)
            fails every resolve closed = a platform-wide retrieval outage (spec-review).

        **What it tests:**
          - An ``output`` backend populated from a ``per_org_knowledge_bases``-shaped map keyed by a
            realistic Auth0 ``org_external_id`` resolves that exact string — the value the Go API sets on
            ``QueryStreamRequest.org_id`` — to its coordinates.
        """
        from techai_webutils.clients.kb_registry.output.registry import OutputBackedKbRegistry

        # An Auth0 org id (the X-Org-ID header value the Go API puts on QueryStreamRequest.org_id).
        org_external_id = "org_aB3xy9Kw2Qz1"
        coords = KbCoordinates("kb-acme", "ds-acme", "acme/")
        # The shape of the bedrock module's per_org_knowledge_bases output, deserialized into the config.
        tf_output_backed_map = {org_external_id: KbBinding(org_external_id, coords, ACTIVE_STATUS)}

        registry = OutputBackedKbRegistry(tf_output_backed_map)

        assert await registry.resolve(org_external_id) == coords


class TestRosterKbRegistry:
    """The ``roster`` backend: per-org routing with a gapless migration FSM (active/migrating/unmapped)."""

    def _shared(self) -> KbCoordinates:
        """The shared KB coordinates a non-active org falls back to (its authoritative KB pre-cutover)."""
        return _coords("kb-shared")

    def _bindings(self) -> dict[str, KbBinding]:
        """Three org bindings: one active (own KB), one migrating, one provisioning."""
        return {
            "org_active": KbBinding("org_active", _coords("kb-a"), ACTIVE_STATUS),
            "org_migrating": KbBinding("org_migrating", _coords("kb-m"), MIGRATING_STATUS),
            "org_provisioning": KbBinding("org_provisioning", _coords("kb-p"), PROVISIONING_STATUS),
        }

    @pytest.mark.asyncio
    async def test_active_org_resolves_to_its_own_kb(self) -> None:
        """Test that an ``active`` org resolves to its OWN per-org KB.

        **Why this test is important:**
          - Post-cutover an org must read its own physically-isolated KB, not the shared one — the whole
            point of per-org isolation.

        **What it tests:**
          - ``resolve("org_active")`` returns the org's own coordinates.
        """
        from techai_webutils.clients.kb_registry.roster.registry import RosterKbRegistry

        registry = RosterKbRegistry(self._bindings(), self._shared())

        assert await registry.resolve("org_active") == _coords("kb-a")

    @pytest.mark.asyncio
    async def test_migrating_and_provisioning_orgs_read_the_shared_kb(self) -> None:
        """Test that a migrating/provisioning org resolves to the SHARED KB (gapless, not fail-closed).

        **Why this test is important:**
          - During migration an org's docs aren't fully in its new KB yet; it must keep reading the shared
            KB it is authoritative for, or retrieval returns an incomplete result (a regression). This is
            the gapless-migration guarantee (design §5) — and it is NOT a fail-closed violation because the
            org is explicitly listed and reads its own current data.

        **What it tests:**
          - ``resolve`` for a ``migrating`` and a ``provisioning`` org both return the shared coordinates.
        """
        from techai_webutils.clients.kb_registry.roster.registry import RosterKbRegistry

        registry = RosterKbRegistry(self._bindings(), self._shared())

        assert await registry.resolve("org_migrating") == self._shared()
        assert await registry.resolve("org_provisioning") == self._shared()

    @pytest.mark.asyncio
    async def test_unmapped_org_fails_closed(self) -> None:
        """Test that an org absent from the roster fails closed (never another tenant's KB, never shared).

        **Why this test is important:**
          - The security invariant: an UNKNOWN org must resolve to nothing — the shared
            fallback is only for EXPLICITLY-listed migrating orgs, never a default for unmapped ones.

        **What it tests:**
          - ``resolve("org_unknown")`` returns ``None``.
        """
        from techai_webutils.clients.kb_registry.roster.registry import RosterKbRegistry

        registry = RosterKbRegistry(self._bindings(), self._shared())

        assert await registry.resolve("org_unknown") is None

    @pytest.mark.asyncio
    async def test_list_active_includes_shared_while_any_org_is_migrating(self) -> None:
        """Test that ``list_active`` yields active orgs' own KBs PLUS the shared KB while any org migrates.

        **Why this test is important:**
          - A migrating org's LIVE uploads must keep indexing into the shared KB it reads; the shared
            KB-sync runner only exists if the shared binding is in the write fan-out set. The shared
            binding's ``(kb,ds)`` must also NOT collide with any per-org binding's lock key,
            or two runners would fight over one advisory lock.

        **What it tests:**
          - ``list_active`` returns the active org's own binding + a shared ``active`` binding; the shared
            binding's coordinates differ from every per-org binding's (distinct lock keys).
        """
        from techai_webutils.clients.kb_registry.roster.registry import RosterKbRegistry

        active = await RosterKbRegistry(self._bindings(), self._shared()).list_active()

        assert {b.tenant_key for b in active} == {"org_active", "shared"}
        assert all(b.status == ACTIVE_STATUS for b in active)
        # The shared binding's (kb,ds) must not collide with any per-org active binding's lock key.
        keys = [(b.coordinates.knowledge_base_id, b.coordinates.data_source_id) for b in active]
        assert len(keys) == len(set(keys))

    @pytest.mark.asyncio
    async def test_list_active_drops_shared_once_all_orgs_active(self) -> None:
        """Test that once every org is ``active`` the shared binding drops out of ``list_active``.

        **Why this test is important:**
          - After cutover the shared KB is no longer read by anyone; keeping its sync runner alive would
            waste an ingestion lane (and the shared KB is decommissioned, design §5 step 4). It must drop
            out of the write fan-out when no org is migrating.

        **What it tests:**
          - With all bindings ``active``, ``list_active`` returns only the per-org bindings (no shared).
        """
        from techai_webutils.clients.kb_registry.roster.registry import RosterKbRegistry

        all_active = {"org_a": KbBinding("org_a", _coords("kb-a"), ACTIVE_STATUS)}
        active = await RosterKbRegistry(all_active, self._shared()).list_active()

        assert {b.tenant_key for b in active} == {"org_a"}


class TestKbRegistryFactory:
    """``kb_registry_from_config`` selects the backend by ``KbRegistryKind`` and fails loudly."""

    @pytest.mark.asyncio
    async def test_shared_is_the_default_backend(self) -> None:
        """Test that the default config builds the shared backend (behavior-preserving default).

        **Why this test is important:**
          - The topology must default to ``shared`` so an environment that hasn't provisioned per-org
            KBs keeps the single-KB behavior (safe rollout, design §6).

        **What it tests:**
          - ``kb_registry_from_config`` with the default kind resolves any key to the shared coords.
        """
        from techai_webutils.clients.kb_registry import KbRegistryConfig, kb_registry_from_config

        registry = kb_registry_from_config(KbRegistryConfig(shared_coordinates=_coords()))

        assert await registry.resolve("any-workspace") == _coords()

    @pytest.mark.asyncio
    async def test_output_kind_builds_the_output_backend(self) -> None:
        """Test that kind='output' with a binding map builds the per-key output-backed registry.

        **Why this test is important:**
          - Per-org routing is selected by config; a factory that ignored the kind would keep every
            environment on the shared KB even after per-org provisioning.

        **What it tests:**
          - ``kb_registry_from_config(output config)`` resolves a mapped key to its own coordinates.
        """
        from techai_webutils.clients.kb_registry import (
            KbRegistryConfig,
            KbRegistryKind,
            kb_registry_from_config,
        )

        registry = kb_registry_from_config(
            KbRegistryConfig(
                kind=KbRegistryKind.OUTPUT,
                bindings={"workspace-a": KbBinding("org-a", _coords("kb-a"), ACTIVE_STATUS)},
            )
        )

        assert await registry.resolve("workspace-a") == _coords("kb-a")

    def test_unknown_kind_raises(self) -> None:
        """Test that an unknown registry kind raises ``ValueError`` (loud wiring bug, not a fallback).

        **Why this test is important:**
          - A silent fallback to ``shared`` on a typo'd kind would route every tenant to the shared KB
            despite per-org being configured — a cross-tenant correctness bug at wiring time.

        **What it tests:**
          - ``kb_registry_from_config`` with a bogus kind raises ``ValueError``.
        """
        from techai_webutils.clients.kb_registry import KbRegistryConfig, kb_registry_from_config

        with pytest.raises(ValueError, match="unknown kb registry kind"):
            kb_registry_from_config(KbRegistryConfig(kind="bogus"))  # type: ignore[arg-type]

    def test_output_kind_without_bindings_raises(self) -> None:
        """Test that the output backend requires a non-empty binding map (loud wiring bug).

        **Why this test is important:**
          - Selecting per-org routing with an empty map would fail EVERY resolve closed — a total
            retrieval outage; the factory must reject the mis-wire at construction, not at request time.

        **What it tests:**
          - ``kb_registry_from_config`` with kind='output' and no bindings raises ``ValueError``.
        """
        from techai_webutils.clients.kb_registry import (
            KbRegistryConfig,
            KbRegistryKind,
            kb_registry_from_config,
        )

        with pytest.raises(ValueError, match="output kb registry requires"):
            kb_registry_from_config(KbRegistryConfig(kind=KbRegistryKind.OUTPUT))

    def test_shared_kind_without_coordinates_raises(self) -> None:
        """Test that the shared backend requires configured coordinates (loud wiring bug).

        **Why this test is important:**
          - The default topology needs the one KB's coordinates; selecting ``shared`` without them
            must fail at construction rather than return a registry that resolves ``None`` for every
            workspace (a silent total-retrieval outage), symmetric to output-without-bindings.

        **What it tests:**
          - ``kb_registry_from_config`` with kind='shared' and no coordinates raises ``ValueError``.
        """
        from techai_webutils.clients.kb_registry import KbRegistryConfig, kb_registry_from_config

        with pytest.raises(ValueError, match="shared kb registry requires"):
            kb_registry_from_config(KbRegistryConfig())

    @pytest.mark.asyncio
    async def test_roster_kind_builds_the_roster_backend(self) -> None:
        """Test that kind='roster' with bindings + shared coords builds the migration-aware registry.

        **Why this test is important:**
          - The gapless per-org migration topology is selected by config; a factory that ignored the
            roster kind would leave migrating orgs failing closed (an outage) or on the shared KB forever.

        **What it tests:**
          - ``kb_registry_from_config(roster config)`` routes an ``active`` org to its own KB and a
            ``migrating`` org to the shared coords.
        """
        from techai_webutils.clients.kb_registry import (
            KbRegistryConfig,
            KbRegistryKind,
            kb_registry_from_config,
        )

        registry = kb_registry_from_config(
            KbRegistryConfig(
                kind=KbRegistryKind.ROSTER,
                shared_coordinates=_coords("kb-shared"),
                bindings={
                    "org_a": KbBinding("org_a", _coords("kb-a"), ACTIVE_STATUS),
                    "org_m": KbBinding("org_m", _coords("kb-m"), MIGRATING_STATUS),
                },
            )
        )

        assert await registry.resolve("org_a") == _coords("kb-a")
        assert await registry.resolve("org_m") == _coords("kb-shared")

    def test_roster_kind_requires_shared_coordinates(self) -> None:
        """Test that the roster backend requires shared_coordinates (the migrating-org fallback KB).

        **Why this test is important:**
          - Without the shared coords a migrating org has nowhere to read; the factory must reject the
            mis-wire at construction, not resolve ``None`` (a read outage) at request time.

        **What it tests:**
          - ``kind='roster'`` with bindings but no shared_coordinates raises ``ValueError``.
        """
        from techai_webutils.clients.kb_registry import (
            KbRegistryConfig,
            KbRegistryKind,
            kb_registry_from_config,
        )

        with pytest.raises(ValueError, match="roster kb registry requires shared_coordinates"):
            kb_registry_from_config(
                KbRegistryConfig(
                    kind=KbRegistryKind.ROSTER,
                    bindings={"org_a": KbBinding("org_a", _coords("kb-a"), ACTIVE_STATUS)},
                )
            )
