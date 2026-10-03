// Usuários & Acessos (Fase G2) — tela admin-only para cadastrar usuários e decidir quais
// servidores cada um pode ver / editar / receber alerta. Grupos de servidores são um
// atalho: em vez de marcar N servidores por usuário, cria-se o grupo uma vez e vincula-o.
import { useCallback, useEffect, useMemo, useState } from "react";
import { Users as UsersIcon, TriangleAlert } from "lucide-react";
import {
  Button,
  Card,
  Badge,
  Checkbox,
  Modal,
  EmptyState,
  PageHeader,
  HelpPanel,
  Tabs,
  FormField,
  ConfirmDialog,
  useToast,
} from "../components";
import {
  listUsers,
  createUser,
  patchUser,
  deleteUser,
  getUserPerms,
  setUserPerms,
  listServerGroups,
  createServerGroup,
  updateServerGroup,
  deleteServerGroup,
  listHosts,
  type UserAdmin,
  type ServerGroup,
  type ServerPerm,
  type UserGroupLink,
  type HostDetail,
  NOME_PAPEL,
} from "../api";
import "./seletor-papel.css";
import { help } from "../help";
import { normalizeDirect, hostsFromGroups } from "./usersPerms";

function hostLabel(h: HostDetail): string {
  return h.display_name && h.display_name.trim() !== "" ? h.display_name : h.hostname;
}

export function Users() {
  const [tab, setTab] = useState("usuarios");
  const [helpOpen, setHelpOpen] = useState(false);
  const [users, setUsers] = useState<UserAdmin[] | null>(null);
  const [unassigned, setUnassigned] = useState<string[]>([]);
  const [groups, setGroups] = useState<ServerGroup[]>([]);
  const [hosts, setHosts] = useState<HostDetail[]>([]);
  const toast = useToast();

  const reload = useCallback(async () => {
    const [u, g, h] = await Promise.all([listUsers(), listServerGroups(), listHosts()]);
    setUsers(u.users);
    setUnassigned(u.hosts_without_permission);
    setGroups(g.groups);
    setHosts(h.hosts);
  }, []);

  useEffect(() => {
    reload().catch(() => toast.error("Não foi possível carregar usuários e grupos."));
  }, [reload, toast]);

  return (
    <div className="page">
      <PageHeader
        title="Usuários & Acessos"
        subtitle="Cadastre usuários e escolha quais servidores cada um pode ver, editar e receber alerta. Grupos de servidores agilizam quando vários usuários compartilham o mesmo conjunto."
        onHelp={() => setHelpOpen(true)}
      />

      <Tabs
        tabs={[
          { id: "usuarios", label: "Usuários" },
          { id: "grupos", label: "Grupos de servidores" },
        ]}
        active={tab}
        onChange={setTab}
      />

      {tab === "usuarios" ? (
        <UsersTab
          users={users}
          unassigned={unassigned}
          groups={groups}
          hosts={hosts}
          onChanged={reload}
        />
      ) : (
        <GroupsTab groups={groups} hosts={hosts} onChanged={reload} />
      )}

      <HelpPanel
        open={helpOpen}
        onClose={() => setHelpOpen(false)}
        title={help.pages.users.title}
        sections={[
          { heading: "O que é esta tela", body: help.pages.users.what },
          {
            heading: "Como usar",
            body: (
              <ol className="help-section__how">
                {help.pages.users.how.map((h, i) => (
                  <li key={i}>{h}</li>
                ))}
              </ol>
            ),
          },
        ]}
      />
    </div>
  );
}

// ─── Aba Usuários ────────────────────────────────────────────────────────────

function UsersTab({
  users,
  unassigned,
  groups,
  hosts,
  onChanged,
}: {
  users: UserAdmin[] | null;
  unassigned: string[];
  groups: ServerGroup[];
  hosts: HostDetail[];
  onChanged: () => Promise<void>;
}) {
  const [creating, setCreating] = useState(false);
  const [permsFor, setPermsFor] = useState<UserAdmin | null>(null);
  const [editing, setEditing] = useState<UserAdmin | null>(null);

  if (users === null) {
    return <p className="help-empty">Carregando…</p>;
  }

  return (
    <div style={{ display: "grid", gap: "var(--sp-3)" }}>
      {unassigned.length > 0 && (
        <div className="authz-banner" role="status">
          <TriangleAlert size={16} aria-hidden />
          <span>
            {unassigned.length === 1
              ? "1 servidor ainda não foi atribuído a nenhum usuário ou grupo, só administradores o veem."
              : `${unassigned.length} servidores ainda não foram atribuídos a nenhum usuário ou grupo, só administradores os veem.`}
          </span>
        </div>
      )}

      <div style={{ display: "flex", justifyContent: "flex-end" }}>
        <Button variant="primary" onClick={() => setCreating(true)}>
          Novo usuário
        </Button>
      </div>

      {users.length === 0 ? (
        <EmptyState
          icon={<UsersIcon size={40} />}
          title="Nenhum usuário cadastrado"
          body="Crie o primeiro usuário e depois escolha quais servidores ele enxerga."
          action={{ label: "Novo usuário", onClick: () => setCreating(true) }}
        />
      ) : (
        <div className="table-scroll">
          <table className="dtable">
            <thead>
              <tr>
                <th>Usuário</th>
                <th>Contato</th>
                <th>Papel</th>
                <th>Status</th>
                <th>Servidores</th>
                <th>Canais pessoais</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {users.map((u) => (
                <tr key={u.id}>
                  <td>
                    <div style={{ display: "flex", flexDirection: "column" }}>
                      <span>{u.full_name || u.username}</span>
                      {u.email && u.email !== (u.full_name || u.username) && (
                        <span style={{ color: "var(--fg-3)", fontSize: "var(--fs-12)" }}>{u.email}</span>
                      )}
                    </div>
                  </td>
                  <td style={{ color: "var(--fg-2)", fontSize: "var(--fs-12)", whiteSpace: "nowrap" }}>
                    {u.phone || "—"}
                  </td>
                  <td>
                    <Badge state={u.role === "admin" ? "info" : u.role === "operador" ? "ok" : "neutral"}>
                      {NOME_PAPEL[u.role] ?? "Leitor"}
                    </Badge>
                  </td>
                  <td>
                    <Badge state={u.disabled ? "crit" : "ok"}>
                      {u.disabled ? "Inativo" : "Ativo"}
                    </Badge>
                  </td>
                  <td className="tabular">
                    {u.role === "admin" ? "todos" : u.host_count}
                  </td>
                  <td>{u.personal_channels.length > 0 ? u.personal_channels.join(", ") : "—"}</td>
                  <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                    {u.role !== "admin" && (
                      <Button onClick={() => setPermsFor(u)}>Permissões</Button>
                    )}{" "}
                    <Button onClick={() => setEditing(u)}>Editar</Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {creating && (
        <CreateUserModal
          onClose={() => setCreating(false)}
          onCreated={async () => {
            setCreating(false);
            await onChanged();
          }}
        />
      )}
      {permsFor && (
        <PermsModal
          user={permsFor}
          groups={groups}
          hosts={hosts}
          onClose={() => setPermsFor(null)}
          onSaved={async () => {
            setPermsFor(null);
            await onChanged();
          }}
        />
      )}
      {editing && (
        <EditUserModal
          user={editing}
          onClose={() => setEditing(null)}
          onSaved={async () => {
            setEditing(null);
            await onChanged();
          }}
        />
      )}
    </div>
  );
}

function CreateUserModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => Promise<void> }) {
  const [fullName, setFullName] = useState("");
  const [email, setEmail] = useState("");
  const [phone, setPhone] = useState("");
  const [password, setPassword] = useState("");
  const [papel, setPapel] = useState("leitor");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const toast = useToast();

  const emailOk = /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.trim());
  const phoneDigits = phone.replace(/\D/g, "");

  const submit = async () => {
    setErr(null);
    if (!fullName.trim()) {
      setErr("Informe o nome da pessoa.");
      return;
    }
    if (!emailOk) {
      setErr("Informe um email válido, ele será o login do usuário.");
      return;
    }
    if (phoneDigits.length < 10) {
      setErr("Informe um celular válido com DDD (ex.: (11) 99999-9999).");
      return;
    }
    if (password.length < 12) {
      setErr("A senha provisória precisa ter ao menos 12 caracteres.");
      return;
    }
    setBusy(true);
    try {
      await createUser({
        full_name: fullName.trim(),
        email: email.trim().toLowerCase(),
        phone: phone.trim(),
        password,
        role: papel,
      });
      toast.success(`Usuário "${fullName.trim()}" criado. Canal de WhatsApp vinculado ao celular.`);
      await onCreated();
    } catch (e) {
      setErr(
        e instanceof Error && /409/.test(e.message)
          ? "Já existe um usuário com esse email."
          : "Não foi possível criar o usuário.",
      );
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title="Novo usuário"
      footer={
        <>
          <Button onClick={onClose}>Cancelar</Button>
          <Button variant="primary" onClick={submit} disabled={busy}>
            {busy ? "Criando…" : "Criar usuário"}
          </Button>
        </>
      }
    >
      <div style={{ display: "grid", gap: "var(--sp-3)" }}>
        <FormField label="Nome">
          <input
            className="field"
            autoFocus
            placeholder="Ex.: Ana Souza"
            value={fullName}
            onChange={(e) => setFullName(e.target.value)}
          />
        </FormField>
        <FormField label="Email" help="É com este email que o usuário faz login.">
          <input
            className="field"
            type="email"
            autoComplete="off"
            placeholder="pessoa@empresa.com.br"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
        </FormField>
        <FormField label="Celular (WhatsApp)" help="Cria um canal pessoal de WhatsApp para os alertas dos servidores liberados a este usuário. Inclua o DDD, o código do país (55) é adicionado automaticamente.">
          <input
            className="field"
            type="tel"
            placeholder="(11) 99999-9999"
            value={phone}
            onChange={(e) => setPhone(e.target.value)}
          />
        </FormField>
        <FormField label="Senha provisória" help="O usuário será obrigado a trocá-la no primeiro acesso.">
          <input
            className="field"
            type="text"
            autoComplete="off"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </FormField>
        <SeletorPapel valor={papel} onChange={setPapel} />
        {err && (
          <span role="alert" style={{ color: "var(--crit)", fontSize: "var(--fs-12)" }}>
            {err}
          </span>
        )}
      </div>
    </Modal>
  );
}

function EditUserModal({ user, onClose, onSaved }: { user: UserAdmin; onClose: () => void; onSaved: () => Promise<void> }) {
  const [papel, setPapel] = useState(user.role === "admin" || user.role === "operador" ? user.role : "leitor");
  const [disabled, setDisabled] = useState(user.disabled);
  const [resetMFA, setResetMFA] = useState(false);
  const [newPass, setNewPass] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const toast = useToast();

  const remove = async () => {
    setErr(null);
    setBusy(true);
    try {
      await deleteUser(user.id);
      toast.success(`Usuário "${user.full_name || user.username}" excluído.`);
      await onSaved();
    } catch (e) {
      setConfirmingDelete(false);
      setErr(
        e instanceof Error && /409/.test(e.message)
          ? "Não é possível excluir o último administrador ativo."
          : "Não foi possível excluir o usuário.",
      );
    } finally {
      setBusy(false);
    }
  };

  const submit = async () => {
    setErr(null);
    const patch: { role?: string; disabled?: boolean; password?: string; reset_mfa?: boolean } = {};
    if (papel !== user.role) patch.role = papel;
    if (disabled !== user.disabled) patch.disabled = disabled;
    if (resetMFA) patch.reset_mfa = true;
    if (newPass.length > 0) {
      if (newPass.length < 12) {
        setErr("A nova senha precisa ter ao menos 12 caracteres.");
        return;
      }
      patch.password = newPass;
    }
    if (Object.keys(patch).length === 0) {
      onClose();
      return;
    }
    setBusy(true);
    try {
      await patchUser(user.id, patch);
      toast.success(`Usuário "${user.username}" atualizado.`);
      await onSaved();
    } catch (e) {
      setErr(
        e instanceof Error && /409/.test(e.message)
          ? "Não é possível remover ou desativar o último administrador ativo."
          : "Não foi possível salvar as alterações.",
      );
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
    <Modal
      open
      onClose={onClose}
      title={`Editar ${user.full_name || user.username}`}
      footer={
        <div className="row" style={{ justifyContent: "space-between", width: "100%" }}>
          <Button className="btn--danger" onClick={() => setConfirmingDelete(true)} disabled={busy}>
            Excluir
          </Button>
          <div className="row">
            <Button onClick={onClose}>Cancelar</Button>
            <Button variant="primary" onClick={submit} disabled={busy}>
              {busy ? "Salvando…" : "Salvar"}
            </Button>
          </div>
        </div>
      }
    >
      <div style={{ display: "grid", gap: "var(--sp-3)" }}>
        <SeletorPapel valor={papel} onChange={setPapel} />
        {user.mfa_ativo && (
          <Checkbox
            checked={resetMFA}
            onChange={setResetMFA}
            label="Redefinir o 2FA (a pessoa perdeu o celular; ela cadastra de novo no próximo acesso)"
          />
        )}
        <Checkbox checked={disabled} onChange={setDisabled} label="Conta desativada (não faz login nem recebe alerta)" />
        <FormField label="Redefinir senha" help="Deixe em branco para manter. Ao redefinir, o usuário troca no próximo acesso.">
          <input
            className="field"
            type="text"
            autoComplete="off"
            placeholder="nova senha provisória"
            value={newPass}
            onChange={(e) => setNewPass(e.target.value)}
          />
        </FormField>
        {err && (
          <span role="alert" style={{ color: "var(--crit)", fontSize: "var(--fs-12)" }}>
            {err}
          </span>
        )}
      </div>
    </Modal>
      <ConfirmDialog
        open={confirmingDelete}
        verb="Excluir"
        target={`o usuário "${user.full_name || user.username}"`}
        consequences="A conta é removida em definitivo, junto com suas permissões, grupos, sessões e o canal pessoal de WhatsApp. O histórico de alertas enviados é preservado."
        danger
        onCancel={() => setConfirmingDelete(false)}
        onConfirm={remove}
      />
    </>
  );
}

// PermsModal edita as permissões diretas (matriz por servidor) e os grupos do usuário.
function PermsModal({
  user,
  groups,
  hosts,
  onClose,
  onSaved,
}: {
  user: UserAdmin;
  groups: ServerGroup[];
  hosts: HostDetail[];
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const [direct, setDirect] = useState<Map<string, ServerPerm>>(new Map());
  const [links, setLinks] = useState<Map<number, UserGroupLink>>(new Map());
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  useEffect(() => {
    getUserPerms(user.id)
      .then((r) => {
        const d = new Map<string, ServerPerm>();
        for (const p of r.direct) d.set(p.hostname, p);
        const l = new Map<number, UserGroupLink>();
        for (const g of r.groups) l.set(g.group_id, g);
        setDirect(d);
        setLinks(l);
        setLoaded(true);
      })
      .catch(() => toast.error("Não foi possível carregar as permissões do usuário."));
  }, [user.id, toast]);

  // Hostnames herdados dos grupos marcados (para sinalizar na matriz).
  const inherited = useMemo(() => hostsFromGroups([...links.values()], groups), [links, groups]);

  const setDirectFlag = (hostname: string, flag: "can_view" | "can_edit" | "notify", value: boolean) => {
    setDirect((prev) => {
      const next = new Map(prev);
      const cur = next.get(hostname) ?? { hostname, can_view: false, can_edit: false, notify: false };
      const updated = { ...cur, [flag]: value };
      // editar/notificar implicam ver; desmarcar ver limpa os outros.
      if (flag === "can_edit" && value) updated.can_view = true;
      if (flag === "notify" && value) updated.can_view = true;
      if (flag === "can_view" && !value) {
        updated.can_edit = false;
        updated.notify = false;
      }
      if (!updated.can_view && !updated.can_edit && !updated.notify) next.delete(hostname);
      else next.set(hostname, updated);
      return next;
    });
  };

  const toggleGroup = (g: ServerGroup, on: boolean) => {
    setLinks((prev) => {
      const next = new Map(prev);
      if (on) next.set(g.id, { group_id: g.id, can_edit: false, notify: false });
      else next.delete(g.id);
      return next;
    });
  };
  const setGroupFlag = (id: number, flag: "can_edit" | "notify", value: boolean) => {
    setLinks((prev) => {
      const next = new Map(prev);
      const cur = next.get(id);
      if (cur) next.set(id, { ...cur, [flag]: value });
      return next;
    });
  };

  const save = async () => {
    setBusy(true);
    try {
      await setUserPerms(user.id, normalizeDirect([...direct.values()]), [...links.values()]);
      toast.success(`Permissões de "${user.username}" salvas.`);
      await onSaved();
    } catch {
      toast.error("Não foi possível salvar as permissões.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      wide
      title={`Permissões, ${user.username}`}
      footer={
        <>
          <Button onClick={onClose}>Cancelar</Button>
          <Button variant="primary" onClick={save} disabled={busy || !loaded}>
            {busy ? "Salvando…" : "Salvar permissões"}
          </Button>
        </>
      }
    >
      {!loaded ? (
        <p className="help-empty">Carregando…</p>
      ) : (
        <div style={{ display: "grid", gap: "var(--sp-4)" }}>
          {groups.length > 0 && (
            <Card title="Grupos de servidores">
              <p style={{ color: "var(--text-2)", fontSize: "var(--fs-13)", marginTop: 0 }}>
                Vincular um grupo concede, de uma vez, todos os servidores dele. As opções editar/receber
                alerta valem para todos os servidores do grupo.
              </p>
              <div style={{ display: "grid", gap: "var(--sp-2)" }}>
                {groups.map((g) => {
                  const link = links.get(g.id);
                  return (
                    <div key={g.id} className="authz-grouprow">
                      <Checkbox
                        checked={!!link}
                        onChange={(v) => toggleGroup(g, v)}
                        label={`${g.name} (${g.hosts.length} servidor${g.hosts.length === 1 ? "" : "es"})`}
                      />
                      {link && (
                        <span className="authz-grouprow__flags">
                          <Checkbox checked={link.can_edit} onChange={(v) => setGroupFlag(g.id, "can_edit", v)} label="editar" />
                          <Checkbox checked={link.notify} onChange={(v) => setGroupFlag(g.id, "notify", v)} label="receber alerta" />
                        </span>
                      )}
                    </div>
                  );
                })}
              </div>
            </Card>
          )}

          <Card title="Servidores (permissão direta)">
            {hosts.length === 0 ? (
              <p className="help-empty">Nenhum servidor no inventário ainda.</p>
            ) : (
              <div className="table-scroll">
                <table className="dtable authz-matrix">
                  <thead>
                    <tr>
                      <th>Servidor</th>
                      <th>Ver</th>
                      <th>Editar</th>
                      <th>Receber alerta</th>
                    </tr>
                  </thead>
                  <tbody>
                    {hosts.map((h) => {
                      const p = direct.get(h.hostname);
                      const fromGroup = inherited.has(h.hostname);
                      return (
                        <tr key={h.hostname}>
                          <td>
                            {hostLabel(h)}
                            {fromGroup && <span className="authz-tag">via grupo</span>}
                          </td>
                          <td>
                            <Checkbox checked={!!p?.can_view} onChange={(v) => setDirectFlag(h.hostname, "can_view", v)} label="" />
                          </td>
                          <td>
                            <Checkbox checked={!!p?.can_edit} onChange={(v) => setDirectFlag(h.hostname, "can_edit", v)} label="" />
                          </td>
                          <td>
                            <Checkbox checked={!!p?.notify} onChange={(v) => setDirectFlag(h.hostname, "notify", v)} label="" />
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
        </div>
      )}
    </Modal>
  );
}

// ─── Aba Grupos ──────────────────────────────────────────────────────────────

function GroupsTab({
  groups,
  hosts,
  onChanged,
}: {
  groups: ServerGroup[];
  hosts: HostDetail[];
  onChanged: () => Promise<void>;
}) {
  const [editing, setEditing] = useState<ServerGroup | null>(null);
  const [creating, setCreating] = useState(false);
  const toast = useToast();

  const remove = async (g: ServerGroup) => {
    if (!window.confirm(`Apagar o grupo "${g.name}"? Os usuários deixam de herdar esses servidores por ele.`)) return;
    try {
      await deleteServerGroup(g.id);
      toast.success(`Grupo "${g.name}" apagado.`);
      await onChanged();
    } catch {
      toast.error("Não foi possível apagar o grupo.");
    }
  };

  return (
    <div style={{ display: "grid", gap: "var(--sp-3)" }}>
      <div style={{ display: "flex", justifyContent: "flex-end" }}>
        <Button variant="primary" onClick={() => setCreating(true)}>
          Novo grupo
        </Button>
      </div>

      {groups.length === 0 ? (
        <EmptyState
          title="Nenhum grupo ainda"
          body="Crie um grupo (ex.: “Servidores Revoada”) com os servidores que costumam ser liberados juntos. Depois vincule o grupo aos usuários."
          action={{ label: "Novo grupo", onClick: () => setCreating(true) }}
        />
      ) : (
        <div className="table-scroll">
          <table className="dtable">
            <thead>
              <tr>
                <th>Grupo</th>
                <th>Servidores</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {groups.map((g) => (
                <tr key={g.id}>
                  <td>{g.name}</td>
                  <td className="tabular">{g.hosts.length}</td>
                  <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                    <Button onClick={() => setEditing(g)}>Editar</Button>{" "}
                    <Button className="btn--danger" onClick={() => remove(g)}>
                      Apagar
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {(creating || editing) && (
        <GroupModal
          group={editing}
          hosts={hosts}
          onClose={() => {
            setCreating(false);
            setEditing(null);
          }}
          onSaved={async () => {
            setCreating(false);
            setEditing(null);
            await onChanged();
          }}
        />
      )}
    </div>
  );
}

function GroupModal({
  group,
  hosts,
  onClose,
  onSaved,
}: {
  group: ServerGroup | null;
  hosts: HostDetail[];
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const [name, setName] = useState(group?.name ?? "");
  const [selected, setSelected] = useState<Set<string>>(new Set(group?.hosts ?? []));
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const toast = useToast();

  const toggle = (hostname: string, on: boolean) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (on) next.add(hostname);
      else next.delete(hostname);
      return next;
    });
  };

  const submit = async () => {
    setErr(null);
    if (!name.trim()) {
      setErr("Informe o nome do grupo.");
      return;
    }
    setBusy(true);
    try {
      const hostsArr = [...selected];
      if (group) await updateServerGroup(group.id, { name: name.trim(), hosts: hostsArr });
      else await createServerGroup(name.trim(), hostsArr);
      toast.success(`Grupo "${name.trim()}" salvo.`);
      await onSaved();
    } catch (e) {
      setErr(e instanceof Error && /409/.test(e.message) ? "Já existe um grupo com esse nome." : "Não foi possível salvar o grupo.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title={group ? `Editar grupo, ${group.name}` : "Novo grupo"}
      footer={
        <>
          <Button onClick={onClose}>Cancelar</Button>
          <Button variant="primary" onClick={submit} disabled={busy}>
            {busy ? "Salvando…" : "Salvar grupo"}
          </Button>
        </>
      }
    >
      <div style={{ display: "grid", gap: "var(--sp-3)" }}>
        <FormField label="Nome do grupo">
          <input className="field" autoFocus value={name} onChange={(e) => setName(e.target.value)} />
        </FormField>
        <div>
          <div style={{ fontSize: "var(--fs-13)", color: "var(--text-2)", marginBottom: "var(--sp-2)" }}>
            Servidores do grupo
          </div>
          {hosts.length === 0 ? (
            <p className="help-empty">Nenhum servidor no inventário ainda.</p>
          ) : (
            <div style={{ display: "grid", gap: "var(--sp-1)", maxHeight: 320, overflowY: "auto" }}>
              {hosts.map((h) => (
                <Checkbox
                  key={h.hostname}
                  checked={selected.has(h.hostname)}
                  onChange={(v) => toggle(h.hostname, v)}
                  label={hostLabel(h)}
                />
              ))}
            </div>
          )}
        </div>
        {err && (
          <span role="alert" style={{ color: "var(--crit)", fontSize: "var(--fs-12)" }}>
            {err}
          </span>
        )}
      </div>
    </Modal>
  );
}

const DESCRICAO_PAPEL: Record<string, string> = {
  admin: "Vê e faz tudo: conexões e credenciais, agentes, usuários.",
  operador: "Roda migrações e deploys nos servidores do seu escopo. Exige 2FA.",
  leitor: "Só visualiza painéis, execuções e relatórios.",
};

// SeletorPapel: os três papéis da ARQUITETURA §14, com o que cada um pode fazer.
function SeletorPapel({ valor, onChange }: { valor: string; onChange: (p: string) => void }) {
  return (
    <fieldset className="seletor-papel">
      <legend>Papel</legend>
      {(["leitor", "operador", "admin"] as const).map((p) => (
        <label key={p} className={`seletor-papel__opcao${valor === p ? " is-ativo" : ""}`}>
          <input type="radio" name="papel" value={p} checked={valor === p} onChange={() => onChange(p)} />
          <span>
            <strong>{NOME_PAPEL[p]}</strong>
            <small>{DESCRICAO_PAPEL[p]}</small>
          </span>
        </label>
      ))}
    </fieldset>
  );
}
