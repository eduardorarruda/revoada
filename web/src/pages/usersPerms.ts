// Helpers puros da tela Usuários & Acessos (testados em usersPerms.test.ts).
// Espelham a regra do backend: permissão efetiva = união das diretas com as herdadas
// dos grupos; editar/notificar implicam ver.
import type { ServerPerm, UserGroupLink, ServerGroup } from "../api";

export interface EffPerm {
  view: boolean;
  edit: boolean;
  notify: boolean;
}

// normalizeDirect garante edit⇒view e remove linhas sem nenhuma flag (não guardamos
// "acesso zero"). É o que a tela envia no PUT.
export function normalizeDirect(perms: ServerPerm[]): ServerPerm[] {
  const out: ServerPerm[] = [];
  for (const p of perms) {
    const view = p.can_view || p.can_edit || p.notify;
    if (!view) continue;
    out.push({ hostname: p.hostname, can_view: true, can_edit: p.can_edit, notify: p.notify });
  }
  return out;
}

// effectiveFromParts calcula, no cliente, a permissão efetiva a partir das permissões
// diretas + os grupos do usuário + a definição dos grupos (para saber os hostnames de
// cada grupo). Serve para mostrar o que é herdado sem novo round-trip.
export function effectiveFromParts(
  direct: ServerPerm[],
  groupLinks: UserGroupLink[],
  groups: ServerGroup[],
): Map<string, EffPerm> {
  const byGroup = new Map<number, ServerGroup>();
  for (const g of groups) byGroup.set(g.id, g);

  const eff = new Map<string, EffPerm>();
  const bump = (host: string, view: boolean, edit: boolean, notify: boolean) => {
    const cur = eff.get(host) ?? { view: false, edit: false, notify: false };
    eff.set(host, {
      view: cur.view || view,
      edit: cur.edit || edit,
      notify: cur.notify || notify,
    });
  };

  for (const p of direct) {
    const view = p.can_view || p.can_edit || p.notify;
    if (view) bump(p.hostname, true, p.can_edit, p.notify);
  }
  for (const link of groupLinks) {
    const g = byGroup.get(link.group_id);
    if (!g) continue;
    for (const host of g.hosts) bump(host, true, link.can_edit, link.notify);
  }
  return eff;
}

// hostsFromGroups devolve o conjunto de hostnames herdados via grupos (para marcar as
// linhas da matriz como "vem do grupo", não editáveis diretamente).
export function hostsFromGroups(groupLinks: UserGroupLink[], groups: ServerGroup[]): Set<string> {
  const byGroup = new Map<number, ServerGroup>();
  for (const g of groups) byGroup.set(g.id, g);
  const out = new Set<string>();
  for (const link of groupLinks) {
    const g = byGroup.get(link.group_id);
    if (g) for (const host of g.hosts) out.add(host);
  }
  return out;
}
