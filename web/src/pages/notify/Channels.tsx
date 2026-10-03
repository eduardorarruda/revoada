// Aba Canais: destinos de notificação com formulário que se adapta ao tipo
// (E-mail/Webhook/Telegram/WhatsApp) — sem JSON cru no fluxo padrão — teste inline e apagar.
// A conexão do WhatsApp (Evolution API) é configurada UMA vez no card de integração;
// os canais de WhatsApp guardam só os destinatários.
import { useCallback, useEffect, useState } from "react";
import { Megaphone, MessageCircle } from "lucide-react";
import {
  AdvancedSection,
  Badge,
  Button,
  Card,
  ConfirmDialog,
  DataTable,
  EmptyState,
  FormField,
  InfoTip,
  Modal,
  useToast,
  IconButton,
  ActionIcons,
} from "../../components";
import {
  createChannel,
  deleteChannel,
  getRole,
  getWhatsappIntegration,
  listChannels,
  listUsers,
  testChannel,
  updateChannel,
  updateWhatsappIntegration,
  type NotificationChannel,
  type UserAdmin,
  type WhatsAppIntegration,
} from "../../api";
import { help } from "../../help";
import { MONO, MUTED, TableOrSkeleton } from "./shared";

// Tipos suportados no formulário guiado (batem com os senders do backend).
type ChannelType = "smtp" | "webhook" | "telegram" | "whatsapp";
const TYPE_LABEL: Record<string, string> = { smtp: "E-mail", webhook: "Webhook", telegram: "Telegram", whatsapp: "WhatsApp" };

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

// Resumo legível da config por tipo (o backend já mascara os segredos).
function configSummary(type: string, config: Record<string, unknown>): string {
  const val = (k: string) => {
    const v = config[k];
    return v == null || v === "" ? "" : String(v);
  };
  if (type === "smtp") {
    const parts = [] as string[];
    if (val("to")) parts.push(`Para ${val("to")}`);
    if (val("host")) parts.push(`via ${val("host")}${val("port") ? `:${val("port")}` : ""}`);
    if (parts.length) return parts.join(" ");
  } else if (type === "webhook") {
    if (val("url")) return val("url");
  } else if (type === "telegram") {
    if (val("chat_id")) return `Chat ${val("chat_id")}`;
  } else if (type === "whatsapp") {
    if (val("to")) return `Para ${val("to")}`;
  }
  // Fallback para tipos legados/exóticos: pares chave=valor.
  const entries = Object.entries(config);
  if (entries.length === 0) return "—";
  return entries.map(([k, v]) => `${k}=${String(v)}`).join("  ");
}

// Botão de acesso à configuração ÚNICA do wuzapi: URL/token definidos uma vez e
// reusados por todos os canais de WhatsApp (que passam a pedir só destinatários).
// Substituiu a Evolution API, que respondia "aceito" para mensagens que o WhatsApp
// recusava.
//
// Era uma FAIXA fixa no topo da tela, acima da tabela — permanentemente ocupando
// espaço para uma configuração que se mexe uma vez na vida e depois nunca mais.
// Agora é um botão ao lado de "Novo canal", com o estado (configurada / não
// configurada) no próprio rótulo, e o formulário abre em modal.
function BotaoIntegracaoWhatsApp({ integration, onSaved }: { integration: WhatsAppIntegration | null; onSaved: () => void }) {
  const toast = useToast();
  const [open, setOpen] = useState(false);
  const configured = integration?.configured ?? false;
  return (
    <>
      <Button
        onClick={() => setOpen(true)}
        title={
          configured
            ? `Conectado a ${integration?.base_url}. Os canais de WhatsApp usam esta conexão, neles basta informar os destinatários.`
            : "Configure o wuzapi uma vez aqui; depois os canais de WhatsApp pedem só os destinatários."
        }
      >
        <MessageCircle size={16} color={configured ? "var(--ok)" : "var(--text-3)"} aria-hidden />
        Integração WhatsApp
        <Badge state={configured ? "ok" : "neutral"}>{configured ? "configurada" : "não configurada"}</Badge>
      </Button>
      {open && (
        <WhatsAppIntegrationForm
          integration={integration}
          onClose={() => setOpen(false)}
          onSaved={() => {
            setOpen(false);
            onSaved();
            toast.success("Integração WhatsApp salva.");
          }}
        />
      )}
    </>
  );
}

function WhatsAppIntegrationForm({
  integration,
  onClose,
  onSaved,
}: {
  integration: WhatsAppIntegration | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const toast = useToast();
  const [baseUrl, setBaseUrl] = useState(integration?.base_url ?? "");
  const [token, setToken] = useState(""); // segredo: sempre em branco
  const [busy, setBusy] = useState(false);
  const configured = integration?.configured ?? false;

  const urlInvalid = baseUrl.trim() !== "" && !/^https?:\/\//i.test(baseUrl.trim());
  const canSubmit = !busy && baseUrl.trim() !== "" && !urlInvalid && (configured || token.trim() !== "");

  async function submit() {
    setBusy(true);
    try {
      await updateWhatsappIntegration({
        base_url: baseUrl.trim().replace(/\/+$/, ""),
        token: token.trim(), // vazio preserva o atual no backend
      });
      onSaved();
    } catch {
      setBusy(false);
      toast.error("Não foi possível salvar a integração.");
    }
  }

  return (
    <Modal
      open
      onClose={onClose}
      title="Integração WhatsApp (wuzapi)"
      footer={
        <>
          <Button onClick={onClose}>Cancelar</Button>
          <Button variant="primary" onClick={submit} disabled={!canSubmit}>
            {busy ? "Salvando…" : "Salvar"}
          </Button>
        </>
      }
    >
      <p style={{ ...MUTED, fontSize: "var(--fs-12)", marginTop: 0 }}>
        Defina aqui a conexão com o wuzapi uma única vez. Todos os canais de WhatsApp vão enviar por ela, cada canal só precisa dos destinatários.
      </p>
      <FormField
        label="URL do wuzapi"
        help={help.fields["channel.whatsapp.baseUrl"]}
        required
        hint="Ex.: https://revoada.exemplo.com.br/wuzapi"
        error={urlInvalid ? "A URL precisa começar com http:// ou https://" : undefined}
      >
        <input className="field" value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} placeholder="https://revoada.exemplo.com.br/wuzapi" autoFocus />
      </FormField>
      <FormField
        label="Token da sessão"
        help={help.fields["channel.whatsapp.token"]}
        required={!configured}
        hint={configured ? "Deixe em branco para manter o token atual." : "Token do usuário criado no wuzapi. Guardado de forma segura; não é exibido depois."}
      >
        <input className="field" type="password" value={token} onChange={(e) => setToken(e.target.value)} placeholder={configured ? "••••••" : "token da sessão"} />
      </FormField>
    </Modal>
  );
}

export function Channels() {
  const toast = useToast();
  const isAdmin = getRole() === "admin";
  const [channels, setChannels] = useState<NotificationChannel[] | null>(null);
  const [creating, setCreating] = useState(false);
  const [editChannel, setEditChannel] = useState<NotificationChannel | null>(null);
  const [toDelete, setToDelete] = useState<NotificationChannel | null>(null);
  const [waIntegration, setWaIntegration] = useState<WhatsAppIntegration | null>(null);
  const [userNames, setUserNames] = useState<Record<number, string>>({});

  const reload = useCallback(() => {
    listChannels().then((r) => setChannels(r.channels)).catch(() => setChannels([]));
  }, []);
  useEffect(() => {
    if (!isAdmin) return;
    listUsers()
      .then((r) => setUserNames(Object.fromEntries(r.users.map((u) => [u.id, u.username]))))
      .catch(() => setUserNames({}));
  }, [isAdmin]);
  const reloadWa = useCallback(() => {
    if (!isAdmin) return;
    getWhatsappIntegration().then(setWaIntegration).catch(() => setWaIntegration(null));
  }, [isAdmin]);
  useEffect(() => reload(), [reload]);
  useEffect(() => reloadWa(), [reloadWa]);

  async function confirmDelete() {
    const c = toDelete;
    if (!c) return;
    setToDelete(null);
    try {
      await deleteChannel(c.id);
      toast.success(`Canal "${c.name}" apagado.`);
      reload();
    } catch {
      toast.error("Não foi possível apagar o canal.");
    }
  }

  return (
    <Card>
      {isAdmin && (
        <div className="row row--end" style={{ marginBottom: "var(--sp-3)", gap: "var(--sp-2)" }}>
          <BotaoIntegracaoWhatsApp integration={waIntegration} onSaved={reloadWa} />
          <Button variant="primary" onClick={() => setCreating(true)}>
            Novo canal
          </Button>
        </div>
      )}

      <TableOrSkeleton data={channels}>
        {(rows) => (
          <DataTable<NotificationChannel>
            rows={rows}
            keyFn={(c) => c.id}
            columns={[
              {
                key: "name",
                label: "Nome",
                render: (c) => (
                  <span className="row" style={{ gap: "var(--sp-2)", alignItems: "center", flexWrap: "wrap" }}>
                    {c.name}
                    {c.user_id != null && (
                      <span title="Recebe só alertas dos servidores deste usuário">
                        <Badge state="info">
                          Pessoal{userNames[c.user_id] ? ` · ${userNames[c.user_id]}` : ""}
                        </Badge>
                      </span>
                    )}
                  </span>
                ),
              },
              { key: "type", label: "Tipo", render: (c) => <Badge state="info">{TYPE_LABEL[c.type] ?? c.type}</Badge> },
              {
                key: "config",
                label: "Configuração",
                hideOnMobile: true,
                render: (c) => <span style={{ ...MUTED, ...MONO }}>{configSummary(c.type, c.config)}</span>,
              },
              { key: "enabled", label: "Estado", render: (c) => <Badge state={c.enabled ? "ok" : "neutral"}>{c.enabled ? "ativo" : "desligado"}</Badge> },
            ]}
            rowActions={
              isAdmin
                ? (c) => (
                    <div className="row" style={{ gap: "var(--sp-2)", flexWrap: "wrap", alignItems: "center" }}>
                      <TestButton channel={c} />
                      <IconButton icon={ActionIcons.edit} label="Editar" onClick={() => setEditChannel(c)} />
                      <IconButton icon={ActionIcons.delete} label="Apagar" onClick={() => setToDelete(c)} />
                    </div>
                  )
                : undefined
            }
            empty={
              <EmptyState
                icon={<Megaphone size={32} strokeWidth={1.5} />}
                title="Nenhum canal ainda"
                body="Um canal é o destino de uma notificação: um e-mail, um webhook, um chat do Telegram ou um WhatsApp. Sem canal, os alertas ficam no sistema mas não avisam ninguém."
                steps={[
                  "Clique em Novo canal",
                  "Escolha o tipo e preencha os campos que aparecerem",
                  "Cadastre e use \"Enviar teste\" para confirmar a entrega",
                ]}
                action={isAdmin ? { label: "Novo canal", onClick: () => setCreating(true) } : undefined}
              />
            }
          />
        )}
      </TableOrSkeleton>

      {creating && (
        <ChannelForm
          waConfigured={waIntegration?.configured ?? false}
          onClose={() => setCreating(false)}
          onSaved={() => {
            setCreating(false);
            reload();
          }}
        />
      )}

      {editChannel && (
        <ChannelForm
          channel={editChannel}
          waConfigured={waIntegration?.configured ?? false}
          onClose={() => setEditChannel(null)}
          onSaved={() => {
            setEditChannel(null);
            reload();
          }}
        />
      )}

      <ConfirmDialog
        open={toDelete !== null}
        verb="Apagar"
        target={`o canal "${toDelete?.name ?? ""}"`}
        consequences="O canal é removido em definitivo. Rotas que usavam só este canal deixam de notificar."
        danger
        onCancel={() => setToDelete(null)}
        onConfirm={confirmDelete}
      />
    </Card>
  );
}

// Botão de teste com resultado inline no próprio card do canal.
function TestButton({ channel }: { channel: NotificationChannel }) {
  const [msg, setMsg] = useState<{ tone: "ok" | "warn" | "crit"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);

  async function run() {
    setBusy(true);
    setMsg(null);
    const r = await testChannel(channel.id).catch((e) => ({ ok: false, status: "error", error: String(e) }));
    setBusy(false);
    // O erro do provedor vem inteiro: no WhatsApp ele explica o que fazer quando a
    // abordagem é recusada, em vez de só mostrar um código.
    //
    // "sem confirmação" não é falha nem sucesso — o provedor aceitou e ninguém
    // garante que chegou. Fica em amarelo, com a instrução, e NÃO some sozinho: era
    // o ✓ verde nesse caso que fazia o teste parecer bem-sucedido sem nada chegar.
    if (!r.ok) {
      const semConfirmacao = r.status === "unverified";
      setMsg({
        tone: semConfirmacao ? "warn" : "crit",
        text: `${semConfirmacao ? "⚠ sem confirmação:" : "✗"} ${r.error ?? "falhou"}`,
      });
      return;
    }
    setMsg({ tone: "ok", text: "✓ enviado" });
    setTimeout(() => setMsg(null), 5000);
  }

  return (
    <span className="row" style={{ gap: "var(--sp-1)", alignItems: "center" }}>
      <Button variant="ghost" onClick={run} disabled={busy}>
        {busy ? "Enviando…" : "Enviar teste"}
      </Button>
      <InfoTip text={help.fields["channel.test"]} title="Enviar teste" />
      {msg && (
        <span
          role={msg.tone === "ok" ? "status" : "alert"}
          style={{ fontSize: "var(--fs-12)", color: `var(--${msg.tone})` }}
        >
          {msg.text}
        </span>
      )}
    </span>
  );
}

// Lê um valor de config como string (para pré-preencher os campos na edição).
function cfgStr(config: Record<string, unknown> | undefined, key: string): string {
  const v = config?.[key];
  if (v == null) return "";
  return typeof v === "string" ? v : String(v);
}

// Formulário guiado por tipo. Constrói o `config` a partir dos campos estruturados;
// a seção "JSON avançado" mescla chaves extras por cima (para tipos legados/exóticos).
// Com `channel` definido, entra em modo edição: pré-preenche os campos não-secretos
// e mantém os segredos (senha/token) em branco — deixar em branco preserva o atual.
function ChannelForm({
  channel,
  waConfigured,
  onClose,
  onSaved,
}: {
  channel?: NotificationChannel;
  waConfigured: boolean;
  onClose: () => void;
  onSaved: () => void;
}) {
  const toast = useToast();
  const editing = channel != null;
  const [name, setName] = useState(channel?.name ?? "");
  const [type, setType] = useState<ChannelType>((channel?.type as ChannelType) ?? "smtp");
  const [busy, setBusy] = useState(false);
  // Canal pessoal: dono opcional. "" = canal da regra (fan-out clássico).
  const [ownerId, setOwnerId] = useState<string>(channel?.user_id != null ? String(channel.user_id) : "");
  const [users, setUsers] = useState<UserAdmin[]>([]);
  useEffect(() => {
    listUsers().then((r) => setUsers(r.users)).catch(() => setUsers([]));
  }, []);

  // E-mail (smtp)
  const [to, setTo] = useState(cfgStr(channel?.config, "to"));
  const [host, setHost] = useState(cfgStr(channel?.config, "host"));
  const [from, setFrom] = useState(cfgStr(channel?.config, "from"));
  const [port, setPort] = useState(cfgStr(channel?.config, "port"));
  const [username, setUsername] = useState(cfgStr(channel?.config, "username"));
  const [password, setPassword] = useState(""); // segredo: sempre em branco

  // Webhook
  const [url, setUrl] = useState(cfgStr(channel?.config, "url"));

  // Telegram
  const [token, setToken] = useState(""); // segredo: sempre em branco
  const [chatId, setChatId] = useState(cfgStr(channel?.config, "chat_id"));

  // WhatsApp (wuzapi): a conexão vem da Integração global; o canal guarda os
  // destinatários e, opcionalmente, o LID de cada um (ver o campo na tela).
  const [waTo, setWaTo] = useState(cfgStr(channel?.config, "to"));
  const [waLid, setWaLid] = useState(cfgStr(channel?.config, "lid"));

  // JSON avançado (opcional)
  const [advJson, setAdvJson] = useState("");

  const recipients = to.split(",").map((s) => s.trim()).filter(Boolean);
  const emailInvalid = type === "smtp" && recipients.length > 0 && recipients.some((r) => !EMAIL_RE.test(r));
  const urlInvalid = type === "webhook" && url.trim() !== "" && !/^https:\/\//i.test(url.trim());
  const chatInvalid = type === "telegram" && chatId.trim() !== "" && !/^-?\d+$/.test(chatId.trim());
  // LID de verdade tem 14 a 16 dígitos. Um canal ficou dias salvo com "1", sem
  // entregar nada — barrar aqui é onde o erro custa mais barato. Vazio é legítimo:
  // nem sempre se sabe o LID de alguém. O backend repete a checagem.
  const lidsInvalidos = waLid
    .split(",")
    .map((s) => s.trim().replace(/@lid$/, ""))
    .filter((s) => s !== "" && !/^\d{14,16}$/.test(s));

  let advError: string | undefined;
  if (advJson.trim()) {
    try {
      const parsed = JSON.parse(advJson);
      if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) advError = "O JSON precisa ser um objeto.";
    } catch {
      advError = "JSON inválido.";
    }
  }

  const canSubmit = (() => {
    if (!name.trim() || busy || advError) return false;
    if (type === "smtp") return recipients.length > 0 && !emailInvalid && host.trim() !== "" && from.trim() !== "";
    if (type === "webhook") return url.trim() !== "" && !urlInvalid;
    if (type === "telegram") return (editing || token.trim() !== "") && chatId.trim() !== "" && !chatInvalid;
    if (type === "whatsapp") return waConfigured && waTo.trim() !== "" && lidsInvalidos.length === 0;
    return false;
  })();

  function buildConfig(): Record<string, unknown> {
    const cfg: Record<string, unknown> = {};
    if (type === "smtp") {
      cfg.to = recipients.join(", ");
      cfg.host = host.trim();
      cfg.from = from.trim();
      if (port.trim()) cfg.port = port.trim();
      if (username.trim()) cfg.username = username.trim();
      if (password) cfg.password = password;
    } else if (type === "webhook") {
      cfg.url = url.trim();
    } else if (type === "telegram") {
      if (token.trim()) cfg.bot_token = token.trim();
      cfg.chat_id = chatId.trim();
    } else if (type === "whatsapp") {
      // Só destinatários: base_url/token vêm da Integração WhatsApp global.
      cfg.to = waTo.trim();
      cfg.lid = waLid.trim();
    }
    if (advJson.trim()) Object.assign(cfg, JSON.parse(advJson));
    return cfg;
  }

  async function submit() {
    setBusy(true);
    try {
      const payload = {
        name: name.trim(),
        type,
        config: buildConfig(),
        user_id: ownerId === "" ? null : Number(ownerId),
      };
      if (channel) {
        await updateChannel(channel.id, payload);
        toast.success(`Canal "${name.trim()}" atualizado.`);
      } else {
        await createChannel(payload);
        toast.success(`Canal "${name.trim()}" criado.`);
      }
      onSaved();
    } catch {
      setBusy(false);
      toast.error(editing ? "Não foi possível atualizar o canal." : "Não foi possível criar o canal.");
    }
  }

  return (
    <Modal
      open
      onClose={onClose}
      title={editing ? "Editar canal" : "Novo canal"}
      footer={
        <>
          <Button onClick={onClose}>Cancelar</Button>
          <Button variant="primary" onClick={submit} disabled={!canSubmit}>
            {editing ? (busy ? "Salvando…" : "Salvar") : busy ? "Criando…" : "Criar canal"}
          </Button>
        </>
      }
    >
      <FormField label="Nome" help={help.fields["channel.name"]} required>
        <input className="field" value={name} onChange={(e) => setName(e.target.value)} placeholder="E-mail Ops" autoFocus />
      </FormField>

      <FormField label="Tipo" help={help.fields["channel.type"]} required>
        <select className="field" value={type} onChange={(e) => setType(e.target.value as ChannelType)}>
          <option value="smtp">E-mail</option>
          <option value="webhook">Webhook</option>
          <option value="telegram">Telegram</option>
          <option value="whatsapp">WhatsApp</option>
        </select>
      </FormField>

      <FormField
        label="Canal pessoal de"
        help={help.fields["channel.owner"]}
        hint="Deixe em “Nenhum (canal da regra)” para o comportamento normal. Vinculado a um usuário, o canal recebe SÓ os alertas dos servidores em que esse usuário tem “Receber alerta”, e não entra no envio geral das regras."
      >
        <select className="field" value={ownerId} onChange={(e) => setOwnerId(e.target.value)}>
          <option value="">Nenhum (canal da regra)</option>
          {users.map((u) => (
            <option key={u.id} value={String(u.id)}>
              {u.username}
            </option>
          ))}
        </select>
      </FormField>

      {type === "smtp" && (
        <>
          <FormField
            label="Destinatários"
            help={help.fields["channel.email.to"]}
            required
            hint="Um ou mais e-mails separados por vírgula."
            error={emailInvalid ? "Há um e-mail em formato inválido." : undefined}
          >
            <input className="field" value={to} onChange={(e) => setTo(e.target.value)} placeholder="ops@empresa.com, plantao@empresa.com" />
          </FormField>
          <div className="grid-2">
            <FormField label="Servidor SMTP" required hint="Host do servidor de e-mail que fará o envio.">
              <input className="field" value={host} onChange={(e) => setHost(e.target.value)} placeholder="smtp.empresa.com" />
            </FormField>
            <FormField label="Remetente (De)" required hint="Endereço que aparece como remetente.">
              <input className="field" value={from} onChange={(e) => setFrom(e.target.value)} placeholder="alertas@empresa.com" />
            </FormField>
          </div>
          <AdvancedSection label="SMTP: porta e autenticação">
            <div className="grid-2">
              <FormField label="Porta" hint="Padrão 587 quando em branco.">
                <input className="field" value={port} onChange={(e) => setPort(e.target.value)} placeholder="587" />
              </FormField>
              <FormField label="Usuário" hint="Deixe em branco se o servidor não exigir login.">
                <input className="field" value={username} onChange={(e) => setUsername(e.target.value)} placeholder="alertas@empresa.com" />
              </FormField>
            </div>
            <FormField label="Senha" hint={editing ? "Deixe em branco para manter a senha atual." : "Guardada de forma segura; não é exibida depois."}>
              <input className="field" type="password" value={password} onChange={(e) => setPassword(e.target.value)} placeholder="••••••" />
            </FormField>
          </AdvancedSection>
        </>
      )}

      {type === "webhook" && (
        <FormField
          label="URL"
          help={help.fields["channel.webhook.url"]}
          required
          hint="Precisa começar com https://"
          error={urlInvalid ? "A URL precisa começar com https://" : undefined}
        >
          <input className="field" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://hooks.empresa.com/revoada" />
        </FormField>
      )}

      {type === "telegram" && (
        <>
          <FormField
            label="Token do bot"
            help={help.fields["channel.telegram.token"]}
            required={!editing}
            hint={editing ? "Deixe em branco para manter o token atual." : undefined}
          >
            <input className="field" type="password" value={token} onChange={(e) => setToken(e.target.value)} placeholder={editing ? "••••••" : "123456:ABC-DEF..."} />
          </FormField>
          <FormField
            label="Chat ID"
            help={help.fields["channel.telegram.chatId"]}
            required
            error={chatInvalid ? "O Chat ID deve ser numérico (grupos começam com -)." : undefined}
          >
            <input className="field" value={chatId} onChange={(e) => setChatId(e.target.value)} placeholder="-1001234567890" />
          </FormField>
          <p style={{ ...MUTED, fontSize: "var(--fs-12)" }}>
            Crie um bot com o <strong>@BotFather</strong> para obter o token; para o Chat ID, adicione o bot ao grupo e consulte as atualizações dele (getUpdates).
          </p>
        </>
      )}

      {type === "whatsapp" && (
        <>
          {!waConfigured && (
            <p role="alert" style={{ color: "var(--warn)", fontSize: "var(--fs-12)", marginTop: 0 }}>
              A <strong>Integração WhatsApp</strong> ainda não foi configurada. Configure-a no topo da aba Canais antes de criar este canal.
            </p>
          )}
          <FormField
            label="Destinatários"
            help={help.fields["channel.whatsapp.to"]}
            required
            hint="Números com DDI (ex.: 5511999999999) ou ID de grupo (…@g.us), separados por vírgula."
          >
            <input className="field" value={waTo} onChange={(e) => setWaTo(e.target.value)} placeholder="5511999999999, 120363000000000000@g.us" />
          </FormField>
          <FormField
            label="LID (recomendado)"
            help={help.fields["channel.whatsapp.lid"]}
            hint="Um LID por destinatário, na MESMA ordem dos números acima. Deixe vazio quem você não souber (ex.: 123456789012345, )."
            error={
              lidsInvalidos.length > 0
                ? `LID inválido: ${lidsInvalidos.join(", ")}. O LID tem de 14 a 16 dígitos (ex.: 123456789012345). Se não souber o da pessoa, deixe em branco.`
                : undefined
            }
          >
            <input className="field" value={waLid} onChange={(e) => setWaLid(e.target.value)} placeholder="123456789012345" />
          </FormField>
          <p style={{ ...MUTED, fontSize: "var(--fs-12)" }}>
            O LID é o que faz o alerta chegar: sem ele o painel envia pelo número, mas
            registra como <strong>sem confirmação</strong>, porque o WhatsApp descarta em
            silêncio mensagem para quem nunca conversou com o número do painel. Para descobrir
            o LID de alguém, peça que a pessoa envie uma mensagem para esse número. O envio
            usa o wuzapi da <strong>Integração WhatsApp</strong>.
          </p>
        </>
      )}

      <AdvancedSection label="JSON avançado (opcional)">
        <FormField
          label="Config extra (JSON)"
          hint="Para power users: chaves aqui sobrescrevem/complementam o formulário acima (ex.: headers de webhook)."
          error={advError}
        >
          <textarea
            className="field"
            rows={4}
            value={advJson}
            onChange={(e) => setAdvJson(e.target.value)}
            placeholder={'{ "headers": { "Authorization": "Bearer ..." } }'}
            style={{ fontFamily: "var(--font-mono)", fontSize: "var(--fs-12)" }}
          />
        </FormField>
      </AdvancedSection>
    </Modal>
  );
}
