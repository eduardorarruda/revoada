import { describe, it, expect } from "vitest";
import { normalizeDirect, effectiveFromParts, hostsFromGroups } from "./usersPerms";
import type { ServerPerm, UserGroupLink, ServerGroup } from "../api";

describe("normalizeDirect", () => {
  it("editar implica ver; remove linhas sem flag", () => {
    const input: ServerPerm[] = [
      { hostname: "a", can_view: false, can_edit: true, notify: false }, // edit⇒view
      { hostname: "b", can_view: false, can_edit: false, notify: true }, // notify⇒view
      { hostname: "c", can_view: false, can_edit: false, notify: false }, // some
      { hostname: "d", can_view: true, can_edit: false, notify: false },
    ];
    const out = normalizeDirect(input);
    expect(out).toEqual([
      { hostname: "a", can_view: true, can_edit: true, notify: false },
      { hostname: "b", can_view: true, can_edit: false, notify: true },
      { hostname: "d", can_view: true, can_edit: false, notify: false },
    ]);
  });
});

const groups: ServerGroup[] = [
  { id: 1, name: "Revoada", hosts: ["a", "b", "c"] },
  { id: 2, name: "Cliente", hosts: ["c", "d"] },
];

describe("effectiveFromParts", () => {
  it("une diretas e herdadas de grupo (compartilhado soma flags)", () => {
    const direct: ServerPerm[] = [{ hostname: "a", can_view: true, can_edit: true, notify: false }];
    const links: UserGroupLink[] = [
      { group_id: 1, can_edit: false, notify: true }, // a,b,c ver+notify
      { group_id: 2, can_edit: true, notify: false }, // c,d ver+editar
    ];
    const eff = effectiveFromParts(direct, links, groups);
    // a: direto editar + grupo1 notify ⇒ view+edit+notify
    expect(eff.get("a")).toEqual({ view: true, edit: true, notify: true });
    // b: só grupo1 ⇒ view+notify
    expect(eff.get("b")).toEqual({ view: true, edit: false, notify: true });
    // c: grupo1(notify) + grupo2(edit) ⇒ view+edit+notify
    expect(eff.get("c")).toEqual({ view: true, edit: true, notify: true });
    // d: grupo2 ⇒ view+edit
    expect(eff.get("d")).toEqual({ view: true, edit: true, notify: false });
  });

  it("sem diretas e sem grupos = mapa vazio", () => {
    expect(effectiveFromParts([], [], groups).size).toBe(0);
  });
});

describe("hostsFromGroups", () => {
  it("junta os hostnames dos grupos do usuário (dedup)", () => {
    const links: UserGroupLink[] = [
      { group_id: 1, can_edit: false, notify: false },
      { group_id: 2, can_edit: false, notify: false },
    ];
    expect([...hostsFromGroups(links, groups)].sort()).toEqual(["a", "b", "c", "d"]);
  });
  it("grupo inexistente é ignorado", () => {
    expect(hostsFromGroups([{ group_id: 99, can_edit: false, notify: false }], groups).size).toBe(0);
  });
});
