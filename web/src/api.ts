// Cliente da API do Revoada. Access token em memória; refresh via cookie httpOnly.
import { drainSSE } from "./sse";
import { marcarFalha, marcarSucesso } from "./conexao";

let accessToken: string | null = null;
let currentRole: string | null = null;

// Escopo de servidores do usuário (de /api/me). Para admin, `all` é true e as listas
// ficam vazias (vê e edita tudo). Para usuário comum, as listas trazem os hostnames
// liberados. Usado só para ESCONDER botões no front — a segurança real é no backend.
interface MyScope {
  all: boolean;
  viewHosts: Set<string>;
  editHosts: Set<string>;
}
let myScope: MyScope = { all: false, viewHosts: new Set(), editHosts: new Set() };

export function getRole(): string | null {
  return currentRole;
}
export function isAdmin(): boolean {
  return currentRole === "admin";
}
// canEditHost/canViewHost: admin sempre pode; usuário comum só nos hosts do escopo.
export function canEditHost(hostname: string): boolean {
  return myScope.all || myScope.editHosts.has(hostname);
}
export function canViewHost(hostname: string): boolean {
  return myScope.all || myScope.viewHosts.has(hostname);
}

// MeResponse reflete /api/me. loadMe() atualiza o papel e o escopo em memória; o boot da
// app o chama após restaurar a sessão. Devolve must_reset_password para o fluxo de senha
// provisória.
export interface MeResponse {
  id: number;
  username: string;
  role: string;
  papel_nome?: string;
  must_reset_password?: boolean;
  mfa_ativo?: boolean;
  mfa_obrigatorio?: boolean;
  sessao_com_mfa?: boolean;
  all?: boolean;
  view_hosts?: string[];
  edit_hosts?: string[];
}
let currentMe: MeResponse | null = null;
export function getMe(): MeResponse | null {
  return currentMe;
}
// podeOperar: administrador ou operador (rodam migrações e deploys). Só para esconder
// botões — o back-end confere o papel em toda ação.
export function podeOperar(): boolean {
  return currentRole === "admin" || currentRole === "operador";
}
export const NOME_PAPEL: Record<string, string> = { admin: "Administrador", operador: "Operador", leitor: "Leitor" };

/** Sonda leve da conexão (o selo do rodapé usa quando a tela está parada). */
export function sondarConexao(): Promise<unknown> {
  return api("/api/me");
}

export async function loadMe(): Promise<MeResponse> {
  const body = await api<MeResponse>("/api/me");
  currentMe = body;
  window.dispatchEvent(new Event("revoada:me"));
  currentRole = body.role ?? currentRole;
  myScope = {
    all: body.all === true,
    viewHosts: new Set(body.view_hosts ?? []),
    editHosts: new Set(body.edit_hosts ?? []),
  };
  return body;
}
export async function changePassword(currentPassword: string, newPassword: string): Promise<void> {
  await api("/api/auth/change-password", {
    method: "POST",
    body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }),
  });
}
export function getAccessToken(): string | null {
  return accessToken;
}
export function isAuthenticated(): boolean {
  return accessToken !== null;
}

async function readAccess(res: Response) {
  const t = res.headers.get("X-Access-Token");
  if (t) accessToken = t;
}

// ResultadoLogin: ou a sessão abriu (mustReset diz se a senha é provisória), ou a conta
// tem 2FA e o login segue para a 2ª etapa com o desafio.
export type ResultadoLogin = { tipo: "ok"; mustReset: boolean } | { tipo: "mfa"; desafio: string };

export async function login(username: string, password: string): Promise<ResultadoLogin> {
  const res = await fetch("/api/auth/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, password }),
  });
  if (!res.ok) {
    // Propaga a mensagem do servidor (ex.: "conta desativada", bloqueio por tentativas);
    // cai num texto genérico se o corpo vier vazio.
    const msg = (await res.text()).trim();
    throw new Error(msg || "credenciais inválidas");
  }
  const body = await res.json();
  if (body.mfa_necessario) return { tipo: "mfa", desafio: body.desafio };
  await readAccess(res);
  currentRole = body.role ?? null;
  return { tipo: "ok", mustReset: body.must_reset_password === true };
}

// verificarMFA é a 2ª etapa do login: código do app (6 dígitos) ou de recuperação.
export async function verificarMFA(desafio: string, codigo: string): Promise<{ mustReset: boolean }> {
  const res = await fetch("/api/auth/mfa/verificar", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ desafio, codigo }),
  });
  if (!res.ok) throw new Error((await res.text()).trim() || "código inválido");
  await readAccess(res);
  const body = await res.json();
  currentRole = body.role ?? null;
  return { mustReset: body.must_reset_password === true };
}

// estadoInicial diz se o sistema ainda não tem nenhum usuário (primeiro acesso).
export async function estadoInicial(): Promise<{ precisaConfigurar: boolean }> {
  const res = await fetch("/api/auth/estado");
  if (!res.ok) return { precisaConfigurar: false };
  const body = await res.json();
  return { precisaConfigurar: body.precisa_configurar === true };
}

// --- 2FA e reautenticação ---
export interface InicioMFA {
  segredo: string;
  uri: string;
  qr: string; // data URL (PNG)
}
export function iniciarMFA(): Promise<InicioMFA> {
  return api<InicioMFA>("/api/auth/mfa/iniciar", { method: "POST" });
}
export function confirmarMFA(codigo: string): Promise<{ codigos_recuperacao: string[] }> {
  return api("/api/auth/mfa/confirmar", { method: "POST", body: JSON.stringify({ codigo }) });
}
export function desativarMFA(): Promise<void> {
  return api("/api/auth/mfa/desativar", { method: "POST" });
}
export function reautenticar(dados: { senha?: string; codigo?: string }): Promise<{ valido_ate: number }> {
  return api("/api/auth/reautenticar", { method: "POST", body: JSON.stringify(dados) }, { semReauth: true });
}

// Quem pede a confirmação de identidade (o ReauthModal registra aqui). Devolve true
// se a pessoa confirmou — aí a chamada original é repetida uma vez.
type PedidoReauth = () => Promise<boolean>;
let pedirReauth: PedidoReauth | null = null;
export function registrarPedidoReauth(fn: PedidoReauth | null) {
  pedirReauth = fn;
}

// ErroAcesso carrega o código do 403 de autorização do back-end.
export class ErroAcesso extends Error {
  constructor(
    public codigo: string,
    mensagem: string,
  ) {
    super(mensagem);
  }
}

export async function register(username: string, password: string): Promise<void> {
  const res = await fetch("/api/auth/register", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, password }),
  });
  if (!res.ok) throw new Error(await res.text());
}

export async function logout(): Promise<void> {
  await fetch("/api/auth/logout", { method: "POST" });
  accessToken = null;
  currentRole = null;
}

// UMA renovação de cada vez, compartilhada por todo mundo que estiver esperando.
//
// O refresh token é de USO ÚNICO: o servidor revoga o antigo ao emitir o novo. Duas
// renovações simultâneas, portanto, garantem que a segunda falhe — e falhar aqui
// derrubava a pessoa para a tela de login. Era o que acontecia depois de um tempo
// parado: o access token vence em 15 minutos, as várias telas em polling levam 401
// na mesma leva, e cada uma disparava a sua própria renovação. O F5 "consertava"
// porque no boot existe uma renovação só.
let renovacaoEmCurso: Promise<boolean> | null = null;

function refresh(): Promise<boolean> {
  if (!renovacaoEmCurso) {
    renovacaoEmCurso = renovar().finally(() => {
      renovacaoEmCurso = null;
    });
  }
  return renovacaoEmCurso;
}

async function renovar(): Promise<boolean> {
  const res = await fetch("/api/auth/refresh", { method: "POST" });
  if (!res.ok) {
    // Só 401/403 são sessão perdida de verdade. 502/503 é servidor reiniciando
    // (um deploy, por exemplo): quem está logado não pode ser expulso por um
    // soluço de infraestrutura. O erro sobe e a tela tenta de novo no ciclo
    // seguinte, com a sessão intacta.
    if (res.status === 401 || res.status === 403) return false;
    throw new Error(`não deu para renovar a sessão agora (${res.status})`);
  }
  await readAccess(res);
  const body = await res.json().catch(() => ({}));
  if (body.role) currentRole = body.role;
  return true;
}

// sessaoRenovada trata o 401 de uma chamada autenticada e diz se vale repetir.
//
// `tokenUsado` é o token com que a chamada saiu: se ele já mudou, outra chamada
// renovou enquanto esta estava no ar e basta repetir — renovar de novo só gastaria
// mais uma rotação. Quando devolve false a sessão está perdida mesmo: o estado é
// limpo e o topo da app é avisado, senão o polling ficaria martelando 401 numa tela
// vazia.
async function sessaoRenovada(tokenUsado: string | null): Promise<boolean> {
  if (accessToken !== tokenUsado) return true;
  if (await refresh()) return true;
  accessToken = null;
  currentRole = null;
  window.dispatchEvent(new Event("revoada:auth-lost"));
  return false;
}

// Recupera a sessão no boot: o access token vive só em memória, mas o refresh
// token está num cookie httpOnly que sobrevive ao reload. Devolve true se
// conseguiu reautenticar (evita jogar o usuário pra tela de login a cada F5).
export function restoreSession(): Promise<boolean> {
  return refresh();
}

// api faz uma chamada autenticada; em 401 tenta um refresh e repete uma vez. Em 403
// "reautenticacao_necessaria", pede a confirmação de identidade e repete uma vez.
export async function api<T>(path: string, init: RequestInit = {}, opts: { semReauth?: boolean } = {}): Promise<T> {
  const doFetch = () =>
    fetch(path, {
      ...init,
      headers: {
        ...(init.headers ?? {}),
        ...(accessToken ? { Authorization: `Bearer ${accessToken}` } : {}),
        ...(init.body ? { "Content-Type": "application/json" } : {}),
      },
    });

  const tokenUsado = accessToken;
  // Todo caminho de saída avisa o módulo de conexão: é daqui que o selo do menu
  // tira "no ar / reconectando". Falha de REDE (o fetch nem volta) e 5xx contam
  // como conexão perdida; 4xx é servidor vivo respondendo "não" e não conta.
  let res: Response;
  try {
    res = await doFetch();
  } catch (e) {
    marcarFalha();
    throw e;
  }
  if (res.status === 401) {
    if (!(await sessaoRenovada(tokenUsado))) throw new Error("401: sessão expirada");
    try {
      res = await doFetch();
    } catch (e) {
      marcarFalha();
      throw e;
    }
  }
  if (res.status === 403 && res.headers.get("Content-Type")?.includes("application/json")) {
    const corpo = (await res.json().catch(() => ({}))) as { codigo?: string; mensagem?: string };
    if (corpo.codigo === "reautenticacao_necessaria" && !opts.semReauth && pedirReauth && (await pedirReauth())) {
      return api<T>(path, init, { semReauth: true });
    }
    if (corpo.codigo === "mfa_necessario") window.dispatchEvent(new Event("revoada:mfa-necessario"));
    throw new ErroAcesso(corpo.codigo ?? "sem_permissao", corpo.mensagem ?? "sem permissão");
  }
  if (!res.ok) {
    marcarFalha(res.status);
    throw new Error(`${res.status}: ${await res.text()}`);
  }
  marcarSucesso();
  await readAccess(res); // reautenticação e 2FA devolvem um token novo
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

// postSSE faz um POST autenticado cuja resposta é text/event-stream e entrega cada
// evento `data: {...}` a onEvent, resolvendo quando o servidor fecha o fluxo. Como
// EventSource só faz GET, lemos o corpo em streaming (ReadableStream + reader) e
// cortamos os eventos com os helpers puros de sse.ts. Mesmo tratamento de 401 do
// `api<T>`: tenta um refresh e repete uma vez.
async function postSSE<E>(path: string, body: unknown, onEvent: (e: E) => void, signal?: AbortSignal): Promise<void> {
  const doFetch = () =>
    fetch(path, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Accept: "text/event-stream",
        ...(accessToken ? { Authorization: `Bearer ${accessToken}` } : {}),
      },
      body: JSON.stringify(body ?? {}),
      signal,
    });

  const tokenUsado = accessToken;
  let res: Response;
  try {
    res = await doFetch();
    if (res.status === 401) {
      if (!(await sessaoRenovada(tokenUsado))) throw new Error("401: sessão expirada");
      res = await doFetch();
    }
  } catch (e) {
    if (!(e instanceof DOMException && e.name === "AbortError")) marcarFalha();
    throw e;
  }
  if (!res.ok) {
    marcarFalha(res.status);
    throw new Error(`${res.status}: ${await res.text()}`);
  }
  if (!res.body) throw new Error("resposta sem corpo, streaming indisponível neste navegador");
  // As consultas de métricas vêm por aqui: sem isto o selo do rodapé envelhecia
  // ("ao vivo · 445s") com os gráficos chegando normalmente.
  marcarSucesso();

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let pending = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    const { events, rest } = drainSSE<E>(pending, decoder.decode(value, { stream: true }));
    pending = rest;
    if (events.length > 0) marcarSucesso();
    for (const e of events) onEvent(e);
  }
  // Fecha o decoder e processa um último bloco sem \n\n final (servidor pode
  // encerrar o fluxo logo após o último evento).
  const tail = pending + decoder.decode();
  const { events } = drainSSE<E>("", tail.endsWith("\n\n") ? tail : `${tail}\n\n`);
  for (const e of events) onEvent(e);
}

// --- chaves de agente (serverkeys) — admin ---
//
// A LISTAGEM NÃO TRAZ MAIS A CHAVE EM CLARO. Antes um único GET /api/agents
// devolvia a serverkey completa de TODA a frota: uma sessão de admin comprometida
// colhia, numa requisição, a credencial de ingestão de todos os servidores. Agora
// vem `serverkey` MASCARADA ("dev-…46"), que serve para o operador reconhecer a
// linha, e um `id` público (sha256[:16] da chave) que é o que as rotas de escrita
// aceitam. Para o texto da chave existe revealAgentKey() — uma chave por vez e
// registrada na auditoria.

// AgentUpdateReport é o último relato de atualização que o agente mandou. `estado`
// é o vocabulário do agente (agent/internal/selfupdate) — ver AGENT_UPDATE_ESTADO.
export interface AgentUpdateReport {
  versao_atual: string;
  os: string;
  arch: string;
  /**
   * Hostname que o AGENTE diz ter — a única ponte confiável entre uma chave de
   * ingestão e uma linha do inventário (`/api/hosts`). O `hostname` da AgentKey é
   * apelido digitado por gente ao criar a chave ("Loja Exemplo"), não identidade de
   * máquina. Ausente enquanto o agente não mandar o campo (a frota 0.7.0 não manda);
   * ausente significa "não sei", nunca "não casa".
   */
  hostname?: string;
  estado?: string;
  motivo?: string;
  erro?: string;
  versao_desejada?: string;
  quando?: string;
}

export interface AgentKey {
  /** Identificador PÚBLICO da chave (sha256[:16]). É o que as rotas de escrita usam. */
  id: string;
  /** MASCARADA ("dev-…46"): identifica, não autentica. Texto real só via revealAgentKey. */
  serverkey: string;
  tenant_id: string;
  hostname: string;
  revoked: boolean;
  last_seen: string | null;
  created_at: string;
  /** Auto-atualização segurada NESTE servidor (freio por host). */
  update_hold?: boolean;
  update_hold_reason?: string;
  update_report?: AgentUpdateReport;
}
export function listAgents(): Promise<{ agents: AgentKey[] }> {
  return api("/api/agents");
}
export function createAgent(hostname: string): Promise<{ serverkey: string; hostname: string }> {
  return api("/api/agents", { method: "POST", body: JSON.stringify({ hostname }) });
}
export function setAgentRevoked(id: string, revoked: boolean): Promise<void> {
  return api("/api/agents/revoke", { method: "POST", body: JSON.stringify({ id, revoked }) });
}
export function deleteAgent(id: string): Promise<void> {
  return api("/api/agents/delete", { method: "POST", body: JSON.stringify({ id }) });
}

// revealAgentKey devolve o texto de UMA serverkey. É POST porque passa pelo
// middleware de auditoria: "quem revelou a chave de qual servidor e quando" fica
// registrado. Chame no CLIQUE de quem precisa do texto, nunca ao carregar a lista —
// carregar a tela não pode virar um pedido de revelação para a frota inteira.
export function revealAgentKey(id: string): Promise<{ id: string; serverkey: string }> {
  return api("/api/agents/reveal", { method: "POST", body: JSON.stringify({ id }) });
}

// --- freio da auto-atualização do agente — admin ---
//
// Sem esta política, o deploy automático do painel trocava o binário de TODA a
// frota em ~1h a cada `git push`, sem canário e sem botão de parada.
export interface AgentUpdatePolicy {
  /** Auto-atualização desligada para a frota inteira. */
  off: boolean;
  /** Versão fixada ("0.9.1"); vazio = sempre a mais nova publicada. */
  pin: string;
  /** Por que está desligada/fixada — é o que o próximo operador vai ler às 3h. */
  motivo: string;
  updated_at: string;
  updated_by: string;
}

export function getAgentUpdatePolicy(): Promise<AgentUpdatePolicy> {
  return api("/api/agent/update-policy");
}

// setAgentUpdatePolicy grava a política GLOBAL. Pin fora do formato "0.9.1" volta
// 400 com a mensagem pronta do backend — repassamos ao usuário como está.
export function setAgentUpdatePolicy(p: { off: boolean; pin: string; motivo: string }): Promise<void> {
  return api("/api/agent/update-policy", { method: "PUT", body: JSON.stringify(p) });
}

// setAgentUpdateHold segura (ou solta) a atualização de UM servidor. O hold do host
// vence o pin global: "segure este servidor" é a decisão mais específica.
export function setAgentUpdateHold(id: string, hold: boolean, motivo: string): Promise<void> {
  return api("/api/agents/update-hold", { method: "POST", body: JSON.stringify({ id, hold, motivo }) });
}

// --- instalador pronto do agente (escolhe o SO e baixa) — admin ---
export type AgentOS = "linux" | "windows" | "macos";

// Rótulos do seletor de sistema operacional, com o que sai de cada um. Ficam aqui
// (e não na tela) porque a extensão do arquivo é decidida pelo backend: é o mesmo
// contrato, e assim os dois lados são conferidos no mesmo lugar.
export const AGENT_OS_OPTIONS: { id: AgentOS; label: string; ext: string; hint: string }[] = [
  { id: "linux", label: "Linux", ext: ".sh", hint: "Roda com sudo. Instala o serviço, os limites de recurso e a coleta de logs do sistema." },
  { id: "windows", label: "Windows", ext: ".exe", hint: "Clique com o botão direito e escolha “Executar como administrador”." },
  // macOS ainda não foi instalado numa máquina real — o binário é compilado e
  // assinado no build, mas o daemon do launchd só foi conferido no papel. Dizer
  // isso na própria escolha evita alguém prometer ao cliente o que não vimos rodar.
  { id: "macos", label: "macOS", ext: ".command", hint: "Abra o Terminal e rode com sudo. Serve para Apple Silicon e Intel. Ainda não testado em um Mac real." },
];

export interface AgentInstallerReq {
  os: AgentOS;
  // hostname gera uma chave NOVA para um servidor novo; agent_id reaproveita uma
  // chave existente (reinstalar sem espalhar chaves órfãs); enroll_token gera o
  // instalador UNIVERSAL a partir de um token já criado. Um dos três.
  hostname?: string;
  /** Id público da chave existente — o caminho preferido: baixar o instalador não
   *  exige mais trazer a serverkey em claro até o navegador. */
  agent_id?: string;
  /** Só compatibilidade: a listagem não devolve mais a chave em claro. */
  serverkey?: string;
  enroll_token?: string;
  probe?: boolean;
}

// --- instalador universal: tokens de inscrição — admin ---
// O instalador universal não carrega chave de ingestão: carrega um token que só
// serve para um servidor novo pedir a SUA chave ao painel. Por isso o mesmo arquivo
// roda em quantas máquinas você quiser, e revogar o token só impede entradas novas
// — quem já entrou continua reportando com a chave própria.
export interface EnrollToken {
  token: string;
  tenant_id: string;
  label: string;
  revoked: boolean;
  uses: number;
  last_used_at: string | null;
  created_by: string;
  created_at: string;
}
export function listEnrollTokens(): Promise<{ tokens: EnrollToken[] }> {
  return api("/api/enroll-tokens");
}
export function createEnrollToken(label: string): Promise<{ token: string; label: string }> {
  return api("/api/enroll-tokens", { method: "POST", body: JSON.stringify({ label }) });
}
export function setEnrollTokenRevoked(token: string, revoked: boolean): Promise<void> {
  return api("/api/enroll-tokens/revoke", { method: "POST", body: JSON.stringify({ token, revoked }) });
}
export function deleteEnrollToken(token: string): Promise<void> {
  return api("/api/enroll-tokens/delete", { method: "POST", body: JSON.stringify({ token }) });
}

// downloadAgentInstaller baixa o instalador e entrega o arquivo ao navegador.
// Não dá para usar um <a href> simples: o access token vive em memória (não em
// cookie), então a requisição precisa passar pelo mesmo aperto de mão do resto da
// API — inclusive o refresh em 401. Daí o fetch + blob + clique sintético.
// Devolve o nome do arquivo salvo, para a tela poder dizer o que aconteceu.
export async function downloadAgentInstaller(req: AgentInstallerReq): Promise<string> {
  const doFetch = () =>
    fetch("/api/agent-installer", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        ...(accessToken ? { Authorization: `Bearer ${accessToken}` } : {}),
      },
      body: JSON.stringify(req),
    });

  const tokenUsado = accessToken;
  let res = await doFetch();
  if (res.status === 401) {
    if (!(await sessaoRenovada(tokenUsado))) throw new Error("401: sessão expirada");
    res = await doFetch();
  }
  if (!res.ok) throw new Error((await res.text()).trim() || `erro ${res.status}`);

  const nome = filenameFromDisposition(res.headers.get("Content-Disposition"));
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  try {
    const a = document.createElement("a");
    a.href = url;
    a.download = nome;
    document.body.appendChild(a);
    a.click();
    a.remove();
  } finally {
    // O objeto só some da memória quando é revogado; o download já foi disparado.
    setTimeout(() => URL.revokeObjectURL(url), 10_000);
  }
  return nome;
}

// filenameFromDisposition tira o nome do arquivo do cabeçalho da resposta. É o
// backend que decide o nome (e a extensão certa do SO); o fallback só existe para
// o download nunca sair sem nome.
export function filenameFromDisposition(header: string | null): string {
  const m = /filename="([^"]+)"/.exec(header ?? "");
  return m?.[1] ?? "instalar-revoada";
}

// --- provisionamento por SSH (Fase G) — admin ---
// Onboarding automático: em vez de colar o comando de instalação por SSH na mão,
// a central conecta no servidor, gera a chave de ingestão, instala o agente e
// sobe o serviço — reportando cada passo por SSE. A credencial (senha/chave) é
// usada para instalar e fica guardada cifrada num cofre para reprovisionar depois.
export type ProvisionAuthType = "password" | "key";
export type ProvisionStepName = "ssh" | "serverkey" | "install" | "service";
// Além dos 4 passos desenhados, o stream emite passos auxiliares: "persist"
// (guardar credencial no cofre, pode falhar sem invalidar a instalação) e
// "done" (evento de fechamento que espelha o resultado final).
export type ProvisionWireStepName = ProvisionStepName | "persist" | "done";
export type ProvisionStepStatus = "ok" | "erro";

// Um evento do fluxo: qual passo, se deu certo e o detalhe legível (ou o erro).
export interface ProvisionStep {
  step: ProvisionWireStepName;
  status: ProvisionStepStatus;
  detail: string;
}

// Corpo do POST /api/provision/start. `secret` é a senha ou a chave PEM conforme
// `auth_type`; nunca é devolvido pela API depois (o cofre guarda cifrado).
export interface ProvisionStartBody {
  name: string;
  host: string;
  port: number;
  user: string;
  auth_type: ProvisionAuthType;
  secret: string;
}

// Servidor já provisionado. `serverkey` vem mascarada; nunca há `secret` aqui.
export interface ProvisionTarget {
  id: number;
  name: string;
  host: string;
  // hostname REAL da máquina (ex.: "srv-03"), coletado no provisionamento e
  // que casa 1:1 com host.hostname do inventário. Pode vir "" em alvos antigos
  // ainda não reprovisionados (Fase G).
  hostname: string;
  user: string;
  status: string;
  last_provisioned_at: string | null;
  serverkey: string;
}

// Inicia o provisionamento; chama onStep a cada passo e resolve ao fim do fluxo.
// A resolução não implica sucesso — inspecione os passos (status "erro").
export function startProvision(body: ProvisionStartBody, onStep: (s: ProvisionStep) => void, signal?: AbortSignal): Promise<void> {
  return postSSE<ProvisionStep>("/api/provision/start", body, onStep, signal);
}

// Reexecuta a instalação num alvo já cadastrado usando a credencial do cofre —
// sem redigitar senha/chave. Também SSE, mesmo formato de passos.
export function updateProvisionTarget(id: number, onStep: (s: ProvisionStep) => void, signal?: AbortSignal): Promise<void> {
  return postSSE<ProvisionStep>(`/api/provision/targets/${id}/update`, {}, onStep, signal);
}

export function listProvisionTargets(): Promise<{ targets: ProvisionTarget[] }> {
  return api("/api/provision/targets");
}

// --- tipos e chamadas de domínio ---
export interface QueryResponse {
  metric: string;
  table: string;
  ts: number[];
  series: { labels: Record<string, string>; values: (number | null)[] }[];
  // Preenchido SÓ quando o servidor calculou uma agregação diferente da pedida —
  // hoje, "Último" numa janela longa, em que os dados resumidos não guardam o
  // último valor e só sobra a média. O painel continua funcionando; o rodapé conta.
  agg_efetivo?: string;
}

export function queryMetric(body: {
  metric: string;
  tenant?: string;
  filters?: Record<string, string>;
  // group_by: chaves de label para colapsar a série (ex.: ["host"] = uma série
  // por servidor). Vazio/ausente mantém o comportamento antigo (série por labels).
  group_by?: string[];
  from: string;
  to: string;
  step: number;
  agg?: string;
}): Promise<QueryResponse> {
  return api<QueryResponse>("/api/query", { method: "POST", body: JSON.stringify(body) });
}

export interface DashboardModel {
  uid: string;
  title: string;
  variables: { name: string; current: string }[];
  timeRange: { from: string; to: string };
  panels: {
    id: number;
    type: string;
    title: string;
    description?: string;
    gridPos: { x: number; y: number; w: number; h: number };
    query: { metric: string; filters?: Record<string, string>; group_by?: string[]; agg?: string };
  }[];
}

export interface Dashboard {
  uid: string;
  title: string;
  folder: string;
  version: number;
  model: DashboardModel;
}

export function getDashboard(uid: string): Promise<Dashboard> {
  return api<Dashboard>(`/api/dashboards/${uid}`);
}

export function listDashboards(): Promise<{ dashboards: { uid: string; title: string; folder: string; version: number }[] }> {
  return api("/api/dashboards");
}

// ---- Dashboards: CRUD + versões + starter ----
export interface DashboardVersion { version: number; created_by: string; created_at: string }

export function createDashboard(d: { uid: string; title: string; folder?: string; model: DashboardModel }): Promise<{ uid: string; version: number }> {
  return api("/api/dashboards", { method: "POST", body: JSON.stringify(d) });
}
export function updateDashboard(uid: string, d: { title: string; folder?: string; model: DashboardModel }): Promise<{ uid: string; version: number }> {
  return api(`/api/dashboards/${uid}`, { method: "PUT", body: JSON.stringify(d) });
}
export function deleteDashboard(uid: string): Promise<void> {
  return api(`/api/dashboards/${uid}`, { method: "DELETE" });
}
export function dashboardVersions(uid: string): Promise<{ versions: DashboardVersion[] }> {
  return api(`/api/dashboards/${uid}/versions`);
}
export function rollbackDashboard(uid: string, version: number): Promise<{ restored_from: number; new_version: number }> {
  return api(`/api/dashboards/${uid}/rollback`, { method: "POST", body: JSON.stringify({ version }) });
}
export function starterDashboard(host: string): Promise<{ uid: string }> {
  return api(`/api/dashboards/starter?host=${encodeURIComponent(host)}`, { method: "POST" });
}

export interface HostDetail {
  hostname: string;
  // Nome amigável definido pelo usuário; vazio quando não há. O `hostname` técnico
  // continua sendo a chave (das métricas e do inventário) — display_name é só exibição.
  display_name?: string;
  os: string;
  kernel: string;
  arch: string;
  cpu_model: string;
  cpu_cores: number;
  ips: string;
  agent_version: string;
  uptime_secs: number;
  last_seen: string;
  up: boolean;
}

export function listHosts(search = ""): Promise<{ hosts: HostDetail[] }> {
  return api(`/api/hosts?search=${encodeURIComponent(search)}`);
}

// updateHost define (ou limpa, com string vazia) o nome amigável de um servidor.
// O hostname técnico permanece imutável.
export function updateHost(hostname: string, displayName: string): Promise<void> {
  return api(`/api/hosts/${encodeURIComponent(hostname)}`, {
    method: "PATCH",
    body: JSON.stringify({ display_name: displayName }),
  });
}

export function hostTimeline(host: string): Promise<{ events: { kind: string; ts: string; title: string }[] }> {
  return api(`/api/events?host=${encodeURIComponent(host)}`);
}

// --- Armazenamento de logs (aba do modal do host) ---
// Uso atual da tabela de logs no ClickHouse vs. um teto configurável, mais o
// detalhamento do host aberto. ts em epoch (segundos); 0 = ausente.
export interface LogStorage {
  limit_bytes: number; // 0 = sem limite configurado
  total_bytes: number; // tamanho da tabela logs no disco
  total_rows: number;
  oldest_ts: number;
  newest_ts: number;
  pct: number; // total_bytes/limit_bytes (0 se sem limite)
  host?: string;
  host_rows?: number;
  host_oldest_ts?: number;
  host_newest_ts?: number;
}

export function logsStorage(host: string): Promise<LogStorage> {
  return api(`/api/logs/storage?host=${encodeURIComponent(host)}`);
}

// setLogsStorageLimit grava o teto (bytes). 0 remove o limite. Admin.
export function setLogsStorageLimit(bytes: number): Promise<{ bytes: number }> {
  return api(`/api/logs/storage-limit`, {
    method: "PUT",
    body: JSON.stringify({ bytes }),
  });
}

// AgentResourceLimits: a "cerca dura" da unit systemd (MemoryMax/CPUQuota/Nice/
// TasksMax) + os soft caps que o próprio agente aplica (GOMEMLIMIT/GOMAXPROCS).
// Aplicados a novos (re)provisionamentos e ao comando de instalação exibido. Admin.
export interface AgentResourceLimits {
  memory_max_mb: number;
  memory_high_mb: number;
  cpu_quota_pct: number;
  nice: number;
  tasks_max: number;
  mem_soft_mb: number;
  max_procs: number;
}

export function getAgentResourceLimits(): Promise<AgentResourceLimits> {
  return api(`/api/agent/resource-limits`);
}

// setAgentResourceLimits grava os limites e devolve os valores efetivos (saneados).
export function setAgentResourceLimits(l: AgentResourceLimits): Promise<AgentResourceLimits> {
  return api(`/api/agent/resource-limits`, { method: "PUT", body: JSON.stringify(l) });
}

// --- Expurgo cirúrgico de logs (LGPD) ---
//
// O backend aceita TRÊS eixos de recorte (server/internal/logs/purge.go) e o front
// só mandava dois deles pela metade: host + data de corte. Consequência prática: com
// um segredo vazado, a única ação disponível era "apagar tudo de X anterior a Y" — a
// destruição em massa que o backend foi escrito justamente para evitar; e as linhas
// SEM rótulo de host (por onde entra o OTLP de terceiro, o caminho mais provável de
// um vazamento) eram inalcançáveis pela tela.
export interface PurgeFilter {
  /** Servidor alvo. Vazio SÓ quando no_host é true. */
  host?: string;
  /** Alvo = linhas SEM rótulo de servidor (labels['host']=''). */
  no_host?: boolean;
  /** Início da janela (ISO). Opcional: ausente = desde sempre. */
  from?: string;
  /** Fim da janela (ISO), exclusivo. Obrigatório. */
  to: string;
  /** Trecho literal do conteúdo da linha (substring, sem diferenciar maiúsculas). */
  body_like?: string;
}

// O que o expurgo NÃO alcança. Exclusão que não chega ao rollup e ao backup é
// adiamento, não exclusão — e o operador precisa ver a lista para não fechar um
// chamado de LGPD com o mesmo dado vivo em metrics_1h por 730 dias.
export interface NotPurgedItem {
  store: string;
  retention: string;
  note: string;
}

export interface PurgePreview {
  rows: number;
  /** O recorte em português, como o backend o entendeu (repetido na tela). */
  scope: string;
  not_purged: NotPurgedItem[];
}

// purgeLogsPreview conta quantas linhas o recorte vai apagar, ANTES de confirmar.
//
// Vai por POST porque `body_like` carrega o próprio segredo procurado: em GET ele
// ia na query string e ficava legível no log de acesso do nginx — uma cópia nova do
// vazamento criada pela tentativa de apagá-lo.
//
// O recuo para a rota antiga (GET com query) foi REMOVIDO: ele só existia enquanto
// `POST /api/logs/purge/preview` não estava publicado, e mantê-lo significava que um
// 405 acidental reabriria em silêncio justamente o caminho que vaza o segredo na
// query string. Verificado contra a API: o POST responde 200 com {rows, scope,
// not_purged}.
export function purgeLogsPreview(f: PurgeFilter): Promise<PurgePreview> {
  return api<PurgePreview>(`/api/logs/purge/preview`, { method: "POST", body: JSON.stringify(f) });
}

// PurgeResult: done=true → concluído (deleted_rows apagadas). done=false com
// mutation_id → segue em segundo plano (acompanhar via purgeStatus). fail_reason
// preenchido → a limpeza falhou. `not_purged` é a lista do que continua existindo
// fora do alcance deste expurgo — a tela a exibe como checklist do que falta à mão.
export interface PurgeResult {
  done: boolean;
  deleted_rows?: number;
  mutation_id?: string;
  fail_reason?: string;
  scope?: string;
  not_purged?: NotPurgedItem[];
  query_log_purged?: boolean;
  query_log_error?: string;
}

export function purgeLogs(f: PurgeFilter): Promise<PurgeResult> {
  return api(`/api/logs/purge`, { method: "POST", body: JSON.stringify(f) });
}

// StuckMutation é uma limpeza que falhou de forma PERMANENTE e continua registrada
// no ClickHouse. Enquanto ela existir, todo expurgo novo bate na guarda de
// concorrência e responde "Já existe uma limpeza em andamento. Aguarde." — para
// sempre. `fail_reason` é o motivo cru do ClickHouse; `command` NÃO vem de
// propósito (ele cita o body_like, ou seja, o próprio dado que se tenta apagar).
export interface StuckMutation {
  mutation_id: string;
  parts_remaining: number;
  fail_reason: string;
  created_at: string;
}

export function purgeLogsStatus(
  id: string,
): Promise<{ done: boolean; parts_remaining: number; fail_reason: string; stuck?: StuckMutation[] }> {
  return api(`/api/logs/purge/status?id=${encodeURIComponent(id)}`);
}

// purgeLogsStuck lista as limpezas travadas (mesma rota, sem `id`).
export async function purgeLogsStuck(): Promise<StuckMutation[]> {
  const r = await api<{ stuck?: StuckMutation[] }>(`/api/logs/purge/status`);
  return r.stuck ?? [];
}

// purgeLogsCancel mata uma mutation travada (KILL MUTATION no backend). Sem isto, a
// única saída era abrir um cliente do ClickHouse à mão, em produção, com alguém
// esperando o cumprimento de um pedido de LGPD.
export function purgeLogsCancel(mutationId: string): Promise<{ cancelled: string }> {
  return api(`/api/logs/purge/cancel`, { method: "POST", body: JSON.stringify({ mutation_id: mutationId }) });
}

// --- Apagar servidor (agente via SSH + todos os dados do painel) ---
// SSH: quando o painel NÃO tem credencial guardada do host, o front envia estas
// credenciais para desinstalar o agente na máquina. Mesmo formato do provisionamento.
export interface DeleteHostSSH {
  host: string;
  port: number;
  user: string;
  auth_type: ProvisionAuthType;
  secret: string;
}

export interface DeleteHostBody {
  display_name?: string;
  provision_target_id?: number; // usa a credencial guardada deste alvo
  ssh?: DeleteHostSSH; // credencial informada na hora (host sem alvo guardado)
  skip_ssh?: boolean; // apagar só os dados (host morto / sem acesso SSH)
  // auto_uninstall pede ao PRÓPRIO agente que se remova, pelo canal de consulta que
  // ele já usa. Sem SSH e sem deixar processo órfão na máquina.
  auto_uninstall?: boolean;
}

export interface DeleteHostResult {
  data_deleted: boolean;
  ssh: string; // "ok" | "pulado" | "pulado (sem credencial)" | "falhou: <motivo>"
  // auto_uninstall é quantas chaves receberam a ordem de auto-desinstalação. Zero
  // significa que não há agente conhecido para avisar — a tela precisa dizer isso
  // em vez de prometer uma remoção que não vai acontecer.
  auto_uninstall?: number;
  agents_removed: number;
  rules_updated: number;
  perms_removed?: number;
  groups_removed?: number;
  ch_tables: number;
  ch_errors?: string[];
  // ch_pending / ch_failed: tabelas cuja mutation do ClickHouse ainda NÃO terminou (ou
  // falhou) quando o backend respondeu. O servidor só espera alguns segundos — volume
  // grande demora muito mais —, então sem estes dois campos a tela declara "apagado"
  // com os dados ainda legíveis.
  ch_pending?: string[];
  ch_failed?: string[];
  postgres_deleted?: boolean;
  postgres_failed?: string[];
  // alerts_closed: alertas abertos daquele host encerrados em silêncio pela exclusão.
  alerts_closed?: number;
  // not_purged: o que a exclusão NÃO alcança (backup, trilha, histórico). Mesmo
  // contrato do expurgo de logs — e pelo mesmo motivo: declarar apagado o que continua
  // guardado é o erro que fecha um pedido de LGPD de mentira.
  not_purged?: NotPurgedItem[];
}

// deleteHost apaga o servidor por completo. Ação de admin, irreversível.
export function deleteHost(hostname: string, body: DeleteHostBody): Promise<DeleteHostResult> {
  return api(`/api/hosts/${encodeURIComponent(hostname)}/delete`, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

// --- Métricas do servidor inteiro (tela Infraestrutura) ---
// Snapshot ao vivo de cada host: os mesmos recursos do Mural de Saúde
// (CPU/RAM/swap) mais disco por montagem, taxa de rede e nº de processos.
// Reaproveita HealthMetric (pct + bytes + semáforo já resolvido pelo backend).
export interface DiskMount {
  mount: string;
  pct: number;
  used_bytes: number;
  total_bytes: number;
  state: "ok" | "warn" | "crit" | "nodata"; // ver HealthMetric.state
  ts?: number; // ver HealthMetric.ts
  step_seconds?: number; // ver HealthMetric.step_seconds
}
export interface HostNet {
  // AUSENTE = a taxa não pôde ser calculada (só um ponto do contador na janela, ou
  // contador reiniciado por reboot). Antes o backend mandava 0 nesses casos e a
  // listagem imprimia "↓ 0 B/s ↑ 0 B/s" para um host que estava sendo medido — o
  // mesmo zero-no-lugar-de-lacuna que cpu/mem/disco/procs já não fazem. Undefined
  // vira travessão na tela; 0 é um zero MEDIDO (host quieto) e continua sendo 0.
  rx_bps?: number; // bytes/s recebidos (download)
  tx_bps?: number; // bytes/s enviados (upload)
}
// Estado de um container do host (Fase C). O backend resolve `status` (semáforo)
// e ordena a lista com as falhas primeiro; o frontend só apresenta.
export interface ContainerStatus {
  name: string;
  image: string;
  state: string; // running | exited | restarting | paused | dead | created
  health: string; // healthy | unhealthy | starting | none (sem healthcheck)
  running: boolean;
  restarts: number;
  cpu_pct?: number;
  mem_pct?: number;
  mem_used_bytes?: number;
  mem_limit_bytes?: number;
  exit_code?: number; // relevante quando state === "exited"
  status: "ok" | "warn" | "crit" | "neutral";
  // ts/step_seconds: quando o container foi medido pela última vez e com que passo.
  // Mesmo contrato de HealthMetric — ver containerFreshness. Sem eles, o número da
  // linha era exibido como atual até a janela do servidor expirar, e aí o container
  // sumia da lista sem que nada dissesse "parei de medir".
  ts?: number;
  step_seconds?: number;
}
export interface HostMetrics {
  host: string;
  // up=false → o host parou de reportar; a tela mostra "sem métricas recentes"
  // em vez de zeros inventados.
  up: boolean;
  uptime_secs: number;
  cpu: HealthMetric;
  mem: HealthMetric;
  swap?: HealthMetric | null; // ausente/null quando a máquina não tem swap
  disks: DiskMount[];
  net: HostNet;
  // procs = null quando a contagem de PIDs não chegou na janela. Distinguir isso de
  // zero importa: o agente OMITE a métrica quando a leitura de /proc falha, e um
  // servidor vivo era desenhado como "Processos 0" — nenhum processo rodando, o que
  // é impossível. Mesmo contrato de cpu/mem/swap: ausência não é zero.
  procs: number | null;
  // Containers do host (Docker). Ausente/vazio quando não há Docker ou nenhum
  // container — a UI simplesmente não mostra a seção.
  containers?: ContainerStatus[];
}
export function hostMetrics(): Promise<{ hosts: HostMetrics[] }> {
  return api("/api/host-metrics");
}

// Estado de um recurso (cpu/ram/disco): percentual + (para ram/disco) bytes
// usados/totais + o semáforo já resolvido pelo backend a partir dos limiares.
export interface HealthMetric {
  pct: number;
  used_bytes?: number; // só mem e disco
  total_bytes?: number; // só mem e disco
  // "nodata" = a métrica NÃO foi medida nesta janela. É diferente de "medida e
  // deu zero": o backend passou a distinguir os dois porque o valor ausente virava
  // 0 e pintava VERDE — o card dizia "CPU 0%, tudo bem" num servidor cuja CPU
  // ninguém estava medindo. Quando vier "nodata", mostre "—", nunca um número.
  state: "ok" | "warn" | "crit" | "nodata";
  // O NÚMERO SOZINHO NÃO CARREGA A IDADE. `ts` (epoch em SEGUNDOS) é o instante da
  // amostra e `step_seconds` é o passo REAL observado na série. Antes, a validade era
  // uma constante escondida no servidor (5 min no host-metrics, 2 min no mural): o
  // coletor de CPU para de emitir quando não consegue medir enquanto RAM e disco
  // seguem chegando, então o host continuava "no ar" e a célula de CPU repetia o
  // último valor por minutos, com a cor do limiar e sem dizer de quando era — um pico
  // crítico já passado seguia vermelho. Com estes dois campos a tela aplica o MESMO
  // frescor por ladrilho dos painéis (metrics/lastPoint.ts) e mostra a idade.
  // Ausentes quando state === "nodata" (não há o que datar).
  ts?: number;
  step_seconds?: number;
}
export interface HealthCard {
  host: string;
  // Estado geral do card = pior entre os recursos e os alertas ativos.
  // "nosignal" = o host parou de reportar.
  state: "ok" | "warn" | "crit" | "nosignal";
  cpu: HealthMetric;
  mem: HealthMetric;
  disk: HealthMetric;
  up: boolean;
  // Containers do host (Docker), mesma forma do /api/host-metrics. Ausente/vazio
  // quando não há Docker ou nenhum container — o Mural simplesmente não mostra a seção.
  containers?: ContainerStatus[];
}

export function healthWall(): Promise<{ cards: HealthCard[] }> {
  return api("/api/health-wall");
}

// --- Limiares de saúde (semáforo) — admin ---
// Um limiar por (hostname, métrica). hostname vazio ("") = limiar GLOBAL, aplicado
// a todos os hosts que não têm override próprio. warn/crit são percentuais (0–100).
export interface HostThreshold {
  hostname: string;
  metric: "cpu" | "mem" | "disk";
  warn: number;
  crit: number;
}
export function listHostThresholds(): Promise<{ thresholds: HostThreshold[] }> {
  return api("/api/host-thresholds");
}
export function saveHostThresholds(thresholds: HostThreshold[]): Promise<void> {
  return api("/api/host-thresholds", { method: "PUT", body: JSON.stringify({ thresholds }) });
}

export function listMetrics(): Promise<{ metrics: string[] }> {
  return api("/api/metrics");
}

export function labelValues(metric: string, label: string): Promise<{ values: string[] }> {
  return api(`/api/label-values?metric=${encodeURIComponent(metric)}&label=${encodeURIComponent(label)}`);
}

// --- Alerting (P4.1) ---
export interface AlertRule {
  id: number;
  name: string;
  metric: string;
  filters: Record<string, string> | null;
  // Escopo por servidor: hostnames aos quais a regra se aplica. Vazio/null = global
  // (todos os servidores que reportam a métrica).
  hosts: string[] | null;
  agg: string;
  condition_op: string;
  threshold: number;
  window_seconds: number;
  for_seconds: number;
  severity: string;
  runbook: string;
  escalation_policy_id: number | null;
  // Canais que esta regra avisa ao disparar. Vazio = todos os canais habilitados.
  channel_ids: number[];
  enabled: boolean;
}

export interface AlertEvent {
  id: number;
  rule_id: number;
  rule_name: string;
  fingerprint: string;
  labels: Record<string, string>;
  state: string;
  value: number;
  severity: string;
  started_at: string;
  ended_at: string | null;
  acked_by: string | null;
  // Só vem preenchido quando alguém encerrou o alerta à mão (admin).
  resolved_by: string | null;
  flapping: boolean;
}

export type NewAlertRule = Omit<AlertRule, "id" | "enabled" | "filters" | "escalation_policy_id" | "hosts"> & {
  filters?: Record<string, string>;
  hosts?: string[];
  escalation_policy_id?: number | null;
};

export function listAlertRules(): Promise<{ rules: AlertRule[] }> {
  return api("/api/alert-rules");
}

export function createAlertRule(rule: NewAlertRule): Promise<{ id: number }> {
  return api("/api/alert-rules", { method: "POST", body: JSON.stringify(rule) });
}

export function updateAlertRule(id: number, rule: NewAlertRule): Promise<{ id: number }> {
  return api(`/api/alert-rules/${id}`, { method: "PUT", body: JSON.stringify(rule) });
}

export function deleteAlertRule(id: number): Promise<void> {
  return api(`/api/alert-rules/${id}`, { method: "DELETE" });
}

export function previewAlertRule(rule: NewAlertRule, days = 7): Promise<{ fires: number; days: number }> {
  return api(`/api/alert-rules/preview?days=${days}`, { method: "POST", body: JSON.stringify(rule) });
}

export function listAlerts(onlyActive = false): Promise<{ alerts: AlertEvent[] }> {
  return api(`/api/alerts${onlyActive ? "?active=1" : ""}`);
}

export function ackAlert(id: number): Promise<{ acked_by: string }> {
  return api(`/api/alerts/${id}/ack`, { method: "POST" });
}
// Encerra um alerta ativo à mão (admin). Use quando o problema não vai se resolver
// sozinho porque o mundo mudou — um container removido de propósito, por exemplo.
// Não silencia a regra: se a condição voltar a valer, um alerta NOVO dispara.
export function resolveAlert(id: number): Promise<{ resolved_by: string }> {
  return api(`/api/alerts/${id}/resolve`, { method: "POST" });
}

// --- Containers ignorados (admin) ---
// Container parado de propósito: o painel para de alertar sobre ele (em todas as
// regras) e o alerta aberto é encerrado. Nada muda no servidor.
export interface IgnoredContainer {
  host: string;
  container: string;
  created_by: string;
  created_at: string;
}
export function listIgnoredContainers(): Promise<{ containers: IgnoredContainer[] }> {
  return api("/api/alerts/ignored-containers");
}
export function ignoreContainer(host: string, container: string): Promise<{ alerts_closed: number }> {
  return api("/api/alerts/ignored-containers", { method: "POST", body: JSON.stringify({ host, container }) });
}
export function unignoreContainer(host: string, container: string): Promise<void> {
  // No caminho (não na query): a auditoria grava o caminho, então registra qual container foi.
  return api(`/api/alerts/ignored-containers/${encodeURIComponent(host)}/${encodeURIComponent(container)}`, {
    method: "DELETE",
  });
}

// --- Notificações (P4.2) ---
export interface NotificationChannel {
  id: number;
  name: string;
  type: string;
  config: Record<string, unknown>;
  enabled: boolean;
  // Canal pessoal: vinculado a um usuário. null = canal da regra (fan-out clássico).
  user_id: number | null;
}

type ChannelPayload = { name: string; type: string; config: Record<string, unknown>; user_id?: number | null };

export function listChannels(): Promise<{ channels: NotificationChannel[] }> {
  return api("/api/notify/channels");
}
export function createChannel(c: ChannelPayload): Promise<{ id: number }> {
  return api("/api/notify/channels", { method: "POST", body: JSON.stringify(c) });
}
export function updateChannel(id: number, c: ChannelPayload): Promise<void> {
  return api(`/api/notify/channels/${id}`, { method: "PUT", body: JSON.stringify(c) });
}
export function deleteChannel(id: number): Promise<void> {
  return api(`/api/notify/channels/${id}`, { method: "DELETE" });
}
// `status` diz o que de fato aconteceu: "sent" = entrega confirmada pelo provedor,
// "queued" = ele aceitou mas não confirmou a tempo (NÃO é sucesso — a tela avisa).
export function testChannel(id: number): Promise<{ ok: boolean; status?: string; error?: string }> {
  return api(`/api/notify/channels/${id}/test`, { method: "POST" });
}

// Integração WhatsApp (Evolution API): config única reusada por todos os canais WhatsApp.
export interface WhatsAppIntegration {
  base_url: string;
  token: string; // vem mascarado ("••••••") quando já configurado
  configured: boolean;
}
export function getWhatsappIntegration(): Promise<WhatsAppIntegration> {
  return api("/api/notify/whatsapp-integration");
}
export function updateWhatsappIntegration(c: { base_url: string; token: string }): Promise<void> {
  return api("/api/notify/whatsapp-integration", { method: "PUT", body: JSON.stringify(c) });
}

// Histórico de envios (auditoria): registro de cada notificação enviada. Exibido num
// modal na tela de Notificações (deixou de ser aba, mas o registro continua vivo).
export interface NotificationLogEntry {
  id: number;
  channel_type: string;
  // Nome do canal e endereçamento (número, e-mail, chat, host do webhook) NO
  // INSTANTE DO ENVIO. Vêm vazios nos registros gravados antes destas colunas
  // existirem — a tela mostra "—" em vez de exibir o destino de hoje ao lado de
  // um envio antigo. Nunca carregam segredo (ver server/internal/notify/destino.go).
  channel_name: string;
  destination: string;
  route_name: string;
  subject: string;
  alert_count: number;
  status: string;
  detail: string;
  sent_at: string;
}
export function notificationLog(): Promise<{ log: NotificationLogEntry[] }> {
  return api("/api/notify/log");
}

// --- Checks de website/LP (P4.4) ---
export interface SiteCheck {
  id: number;
  name: string;
  url: string;
  tier: string;
  expect_status: number;
  keyword: string;
  max_latency_ms: number;
  enabled: boolean;
  state: string;
  consecutive_fails: number;
  last_checked_at: string | null;
  last_diagnosis: string;
  down_since: string | null;
  uptime_day: number;
  uptime_month: number;
  sparkline?: number[];
  group_name?: string;
  kind?: string; // "http" | "sitemap"
  parent_id?: number | null;
  last_latency?: number; // só no overview (ms da última medição)
  // Canais que este site avisa ao cair/degradar. Vazio = usa as Rotas de notificação.
  channel_ids?: number[] | null;
}

export interface SiteCheckResult {
  ts: string;
  ok: boolean;
  status: number;
  diagnosis: string;
  dns_ms: number;
  connect_ms: number;
  tls_ms: number;
  ttfb_ms: number;
  total_ms: number;
  cert_days_left: number;
}

export function listSiteChecks(): Promise<{ checks: SiteCheck[] }> {
  return api("/api/site-checks");
}
// siteOverview traz TODOS os checks (páginas avulsas + páginas descobertas por
// sitemaps), com uptime e última latência, para a aba "Visão geral".
export function siteOverview(): Promise<{ checks: SiteCheck[] }> {
  return api("/api/site-checks/overview");
}
export interface SiteCheckInput {
  name: string;
  url: string;
  tier: string;
  expect_status: number;
  keyword?: string;
  max_latency_ms?: number;
  group_name?: string;
  probe_locations?: string[];
  kind?: string;
  channel_ids?: number[];
}
export function createSiteCheck(c: SiteCheckInput): Promise<{ id: number }> {
  return api("/api/site-checks", { method: "POST", body: JSON.stringify(c) });
}
export function updateSiteCheck(id: number, c: SiteCheckInput): Promise<void> {
  return api(`/api/site-checks/${id}`, { method: "PUT", body: JSON.stringify(c) });
}
export function deleteSiteCheck(id: number): Promise<void> {
  return api(`/api/site-checks/${id}`, { method: "DELETE" });
}
export function siteCheckHistory(
  id: number,
  range?: { from: string; to: string },
): Promise<{ history: SiteCheckResult[]; uptime_day: number; uptime_month: number; min_ts?: string }> {
  const qs = range ? `?from=${encodeURIComponent(range.from)}&to=${encodeURIComponent(range.to)}` : "";
  return api(`/api/site-checks/${id}/history${qs}`);
}

export interface ProbeResult {
  location: string;
  up: boolean;
  total_ms: number;
  diagnostic: string;
  reported_at: string;
}

// --- Jornadas de browser (P6.4) ---
export interface JourneyStep {
  name: string;
  method: string;
  url: string;
  form?: Record<string, string>;
  assert_status?: number;
  assert_contains?: string;
}
export interface Journey {
  id: number;
  name: string;
  steps: JourneyStep[];
  interval_seconds: number;
  enabled: boolean;
  state: string;
  last_diagnosis: string;
  last_checked_at: string | null;
}
export function listJourneys(): Promise<{ journeys: Journey[] }> {
  return api("/api/journeys");
}
export function createJourney(j: { name: string; interval_seconds: number; steps: JourneyStep[] }): Promise<{ id: number }> {
  return api("/api/journeys", { method: "POST", body: JSON.stringify(j) });
}
export function deleteJourney(id: number): Promise<void> {
  return api(`/api/journeys/${id}`, { method: "DELETE" });
}
export function siteProbes(url: string): Promise<{ probes: ProbeResult[] }> {
  return api(`/api/site-checks/probes?url=${encodeURIComponent(url)}`);
}

// --- Logs (P5.2) ---
export interface LogRow {
  t: number; // epoch ms
  service: string;
  severity: string;
  severity_num: number;
  body: string;
  labels: Record<string, string>;
  trace_id: string;
  span_id: string;
}

type RawLogRow = Record<string, unknown>;
function normLog(r: RawLogRow): LogRow {
  return {
    t: Number(r.t),
    service: String(r.service ?? ""),
    severity: String(r.severity ?? ""),
    severity_num: Number(r.severity_num ?? 0),
    body: String(r.body ?? ""),
    labels: (r.labels as Record<string, string>) ?? {},
    trace_id: String(r.trace_id ?? ""),
    span_id: String(r.span_id ?? ""),
  };
}

export interface LogQuery {
  q?: string;
  service?: string;
  severity?: string;
  host?: string;
  container?: string;
  source?: string;
  from?: string;
  to?: string;
  limit?: number;
}

function logParams(f: LogQuery): string {
  const p = new URLSearchParams();
  if (f.q) p.set("q", f.q);
  if (f.service) p.set("service", f.service);
  if (f.severity) p.set("severity", f.severity);
  if (f.host) p.set("host", f.host);
  if (f.container) p.set("container", f.container);
  if (f.source) p.set("source", f.source);
  if (f.from) p.set("from", f.from);
  if (f.to) p.set("to", f.to);
  if (f.limit) p.set("limit", String(f.limit));
  return p.toString();
}

export async function searchLogs(f: LogQuery): Promise<LogRow[]> {
  const r = await api<{ rows: RawLogRow[] }>(`/api/logs/search?${logParams(f)}`);
  return (r.rows ?? []).map(normLog);
}

// Um bucket do histograma de volume: uma janela de tempo (`t`) para um servidor
// (`host`). O backend quebra a contagem por host, então há um bucket por (t, host);
// a UI empilha as séries por host e monta a legenda. host="" = sem rótulo de host.
export interface LogBucket {
  t: number;
  host: string;
  c: number;
  errors: number;
}
export async function logHistogram(f: LogQuery): Promise<{ buckets: LogBucket[]; step: number }> {
  const r = await api<{ buckets: RawLogRow[]; step: number }>(`/api/logs/histogram?${logParams(f)}`);
  return {
    step: r.step,
    buckets: (r.buckets ?? []).map((b) => ({ t: Number(b.t), host: String(b.host ?? ""), c: Number(b.c), errors: Number(b.errors) })),
  };
}

export interface LogPattern {
  pattern: string;
  c: number;
  sample: string;
  sev: number;
}
export async function logPatterns(f: LogQuery): Promise<LogPattern[]> {
  const r = await api<{ patterns: RawLogRow[] }>(`/api/logs/patterns?${logParams(f)}`);
  return (r.patterns ?? []).map((p) => ({ pattern: String(p.pattern), c: Number(p.c), sample: String(p.sample), sev: Number(p.sev) }));
}

export async function logContext(service: string, ts: number, around = 10): Promise<LogRow[]> {
  const r = await api<{ rows: RawLogRow[] }>(`/api/logs/context?service=${encodeURIComponent(service)}&ts=${ts}&around=${around}`);
  return (r.rows ?? []).map(normLog);
}

export function logServices(): Promise<{ services: string[] }> {
  return api("/api/logs/services");
}

// Fontes de log disponíveis (journald, docker, syslog, kernel, file...). Alimenta
// o filtro "Fonte" da tela de Logs; sem dados, o backend devolve lista vazia.
export function logSources(): Promise<{ sources: string[] }> {
  return api("/api/logs/sources");
}

export interface LogMetric {
  id: number;
  name: string;
  metric_name: string;
  service: string;
  severity_min: number;
  query: string;
  enabled: boolean;
}
export function listLogMetrics(): Promise<{ log_metrics: LogMetric[] }> {
  return api("/api/logs/metrics");
}
export function createLogMetric(m: Omit<LogMetric, "id" | "enabled">): Promise<{ id: number }> {
  return api("/api/logs/metrics", { method: "POST", body: JSON.stringify(m) });
}
export function deleteLogMetric(id: number): Promise<void> {
  return api(`/api/logs/metrics/${id}`, { method: "DELETE" });
}

// openLogTail abre o WebSocket de live tail; devolve um closer.
export function openLogTail(f: LogQuery, onRows: (rows: LogRow[]) => void): () => void {
  const token = getAccessToken();
  const proto = location.protocol === "https:" ? "wss" : "ws";
  const qs = logParams(f);
  const ws = new WebSocket(`${proto}://${location.host}/api/logs/tail?access=${encodeURIComponent(token ?? "")}${qs ? "&" + qs : ""}`);
  ws.onmessage = (ev) => {
    try {
      const msg = JSON.parse(ev.data);
      if (msg.type === "logs" && Array.isArray(msg.rows)) onRows(msg.rows.map(normLog));
    } catch {
      /* ignora frame malformado */
    }
  };
  return () => ws.close();
}

// --- Status page pública (P6.4, sem auth) ---
//
// CONTRATO REAL (server/internal/statuspage/statuspage.go): o uptime NÃO é um
// número solto. O backend publica um objeto com o percentual, a cobertura da
// medição e o número de sondagens, e `percent` vem `null` quando a cobertura não
// autoriza afirmar disponibilidade nenhuma — antes um período SEM sondagem virava
// "100,00%" numa página pública, sem login.
//
// Este tipo declarava `uptime_day: number` e a StatusPage reprovava todo valor no
// guarda `typeof value === "number"`: a página pública NUNCA mostrou uptime, só
// "—". Ver o mesmo contrato já descrito em src/pages/Websites.tsx.
export interface PublicUptime {
  percent: number | null; // null = sem dados suficientes (NUNCA assuma 100)
  coverage: number; // % do período efetivamente sondado
  samples: number; // sondagens observadas no período
}
export interface PublicStatusSite {
  name: string;
  group: string;
  url: string;
  state: string;
  kind: string;
  // Só em kind='sitemap': o check-pai não sonda nada, então mostra páginas no ar
  // em vez de percentual.
  pages?: { total: number; down: number; degraded: number } | null;
  // Nulos quando o check não tem uptime próprio (sitemap). `uptime_90d` também é
  // nulo INTEIRO quando o histórico retido não cobre 90 dias — publicar "90d"
  // sobre 26 dias de histórico é inventar o resto do trimestre.
  uptime_day: PublicUptime | null;
  uptime_month: PublicUptime | null;
  uptime_90d: PublicUptime | null;
}
export interface PublicStatus {
  overall: string;
  updated_at: string;
  sites: PublicStatusSite[];
}
export async function publicStatus(): Promise<PublicStatus> {
  const res = await fetch("/api/status");
  if (!res.ok) throw new Error(String(res.status));
  return res.json();
}

// --- Auto-discovery (P6.2) ---
export interface DiscoverySuggestion {
  host: string;
  kind: string;
  uid: string;
  title: string;
  exists: boolean;
}
export function discoverySuggestions(): Promise<{ suggestions: DiscoverySuggestion[] }> {
  return api("/api/discovery/suggestions");
}
export function applyDiscovery(host: string, kind: string): Promise<{ uid: string }> {
  return api("/api/discovery/apply", { method: "POST", body: JSON.stringify({ host, kind }) });
}

// ---- Discovery raw list ----
export interface HostService { hostname: string; kind: string; detail: string; source: string; discovered_at: string }
export function listDiscovery(): Promise<{ services: HostService[] }> {
  return api("/api/discovery");
}

// --- Traces (P5.3) ---

// TRACE_PARTIAL_LABEL é o rótulo que o gateway coloca nos spans de um trace que
// PERDEU spans antes de a decisão de amostragem virar "manter"
// (gateway/internal/otlp/tracedecision.go, SpanPartialLabel). O valor é sempre "1"
// e só existe quando o trace está incompleto.
//
// Consequência para a leitura da tela, e é por isso que ela precisa avisar: num
// trace parcial o `root_name` pode NÃO ser a raiz de verdade (o servidor promove a
// span mais antiga que sobrou, com argMin(name, ts)) e a duração é um PISO, nunca o
// tempo total — o pedaço descartado não volta.
export const TRACE_PARTIAL_LABEL = "revoada.trace_partial";

export interface TraceSummary {
  trace_id: string;
  start_ms: number;
  duration_ms: number;
  root_service: string;
  root_name: string;
  spans: number;
  has_error: number;
  /** Trace incompleto (spans descartados pela amostragem antes de ele virar "manter"). */
  partial: boolean;
}
export interface TraceSpan {
  start_ms: number;
  duration_ms: number;
  service: string;
  name: string;
  kind: string;
  span_id: string;
  parent_span_id: string;
  status_code: string;
  status_msg: string;
  labels: Record<string, string>;
}
export interface ServiceEdge {
  src: string;
  dst: string;
  calls: number;
  errors: number;
  avg_ms: number;
}

interface TraceQuery {
  service?: string;
  status?: string;
  min_duration?: string;
  host?: string;
  from?: string;
}
function num(v: unknown): number {
  return Number(v ?? 0);
}
function str(v: unknown): string {
  return String(v ?? "");
}

// flag lê um booleano que o ClickHouse pode devolver como 1/0, "1"/"0" ou true.
// Ausente = false: a marca de "parcial" só aparece quando alguém a afirmou — sem
// campo, nunca inventamos que um trace está incompleto.
export function flag(v: unknown): boolean {
  if (v === true) return true;
  if (typeof v === "number") return v === 1;
  if (typeof v === "string") return v === "1" || v === "true";
  return false;
}

// spansSaoParciais decide se um trace aberto está incompleto: basta UM span com o
// rótulo (o gateway o coloca em todos os spans daquele trace, mas os que chegaram
// antes da virada da decisão podem não tê-lo).
export function spansSaoParciais(spans: TraceSpan[]): boolean {
  return spans.some((s) => s.labels?.[TRACE_PARTIAL_LABEL] === "1");
}

export async function searchTraces(f: TraceQuery): Promise<TraceSummary[]> {
  const p = new URLSearchParams();
  Object.entries(f).forEach(([k, v]) => v && p.set(k, v));
  const r = await api<{ traces: Record<string, unknown>[] }>(`/api/traces/search?${p.toString()}`);
  return (r.traces ?? []).map((t) => ({
    trace_id: str(t.trace_id), start_ms: num(t.start_ms), duration_ms: num(t.duration_ms),
    root_service: str(t.root_service), root_name: str(t.root_name), spans: num(t.spans), has_error: num(t.has_error),
    // `partial` ainda não vem do backend (ver TraceSummary). Lemos os dois nomes
    // plausíveis para que a coluna acenda sozinha no dia em que o GROUP BY passar a
    // agregar o rótulo, sem precisar de outro deploy do front.
    partial: flag(t.partial ?? t.trace_partial),
  }));
}

export async function getTrace(traceId: string): Promise<TraceSpan[]> {
  const r = await api<{ spans: Record<string, unknown>[] }>(`/api/traces/${traceId}`);
  return (r.spans ?? []).map((s) => ({
    start_ms: num(s.start_ms), duration_ms: num(s.duration_ms), service: str(s.service), name: str(s.name),
    kind: str(s.kind), span_id: str(s.span_id), parent_span_id: str(s.parent_span_id),
    status_code: str(s.status_code), status_msg: str(s.status_msg), labels: (s.labels as Record<string, string>) ?? {},
  }));
}

// logsForTrace traz os logs cujo trace_id bate exatamente com o trace aberto
// (correlação exata log↔trace; aproveita o bloom_filter da coluna trace_id).
export async function logsForTrace(traceId: string): Promise<LogRow[]> {
  const r = await api<{ rows: RawLogRow[] }>(`/api/traces/${traceId}/logs`);
  return (r.rows ?? []).map(normLog);
}

export interface CorrelationData {
  service: string;
  from: number;
  to: number;
  logs: LogRow[];
  traces: TraceSummary[];
  deploys: { t: number; title: string; body: string; labels: Record<string, string> }[];
}
export async function correlate(opts: { service?: string; host?: string; ts?: number; from?: string; to?: string }): Promise<CorrelationData> {
  const p = new URLSearchParams();
  if (opts.service) p.set("service", opts.service);
  if (opts.host) p.set("host", opts.host);
  if (opts.ts) p.set("ts", String(Math.floor(opts.ts / 1000)));
  if (opts.from) p.set("from", opts.from);
  if (opts.to) p.set("to", opts.to);
  const r = await api<{ service: string; from: number; to: number; logs: Record<string, unknown>[]; traces: Record<string, unknown>[]; deploys: Record<string, unknown>[] }>(`/api/correlate?${p.toString()}`);
  return {
    service: r.service, from: r.from, to: r.to,
    logs: (r.logs ?? []).map(normLog),
    traces: (r.traces ?? []).map((t) => ({
      trace_id: str(t.trace_id), start_ms: num(t.start_ms), duration_ms: num(t.duration_ms),
      root_service: str(t.root_service), root_name: str(t.root_name), spans: num(t.spans), has_error: num(t.has_error),
      partial: flag(t.partial ?? t.trace_partial),
    })),
    deploys: (r.deploys ?? []).map((d) => ({ t: num(d.t), title: str(d.title), body: str(d.body), labels: (d.labels as Record<string, string>) ?? {} })),
  };
}

// openCorrelation dispara o painel lateral de correlação de qualquer tela.
export interface CorrelationRequest {
  service?: string;
  host?: string; // escopa logs/traces a um servidor (labels['host']) — correlação por métrica
  tsMs?: number;
  traceId?: string;
  label?: string;
}
export function openCorrelation(req: CorrelationRequest): void {
  window.dispatchEvent(new CustomEvent("revoada:correlate", { detail: req }));
}

export async function serviceMap(from?: string): Promise<ServiceEdge[]> {
  const r = await api<{ edges: Record<string, unknown>[] }>(`/api/traces/service-map${from ? `?from=${from}` : ""}`);
  return (r.edges ?? []).map((e) => ({
    src: str(e.src), dst: str(e.dst), calls: num(e.calls), errors: num(e.errors), avg_ms: num(e.avg_ms),
  }));
}

// PURGE_TRACES_CONFIRM é a frase que o servidor exige NO CORPO para apagar a tabela
// inteira. A confirmação existia só na tela, e confirmação que mora no cliente não é
// confirmação: qualquer chamada direta à rota (script, curl, aba aberta por engano)
// apagava 15 dias de rastreamento de toda a base sem nada a impedir.
export const PURGE_TRACES_CONFIRM = "APAGAR TODOS OS TRACES";

// purgeTraces apaga TODOS os traces (tabela spans). Admin-only. Devolve quantas linhas
// foram removidas. Para apagar só um recorte, use purgeTracesScoped.
export function purgeTraces(): Promise<{ done: boolean; deleted_rows: number }> {
  return api(`/api/traces/purge`, {
    method: "POST",
    body: JSON.stringify({ confirm: PURGE_TRACES_CONFIRM }),
  });
}

// purgeTracesScoped apaga um RECORTE de traces em vez da tabela inteira — que era o
// único caminho existente e, por isso, o procedimento real de resposta a um segredo
// vazado num span: destruir o rastreamento de todos os clientes. `body_like` casa
// nome, serviço, status e as chaves e valores dos labels, que é onde a instrumentação
// automática guarda header, URL e query string. Com `dry_run`, só conta.
export function purgeTracesScoped(opts: {
  host?: string;
  noHost?: boolean;
  from?: string;
  to?: string;
  bodyLike?: string;
  dryRun?: boolean;
}): Promise<{ done: boolean; deleted_rows: number; mutation_id?: string }> {
  return api(`/api/traces/purge`, {
    method: "POST",
    body: JSON.stringify({
      host: opts.host,
      no_host: opts.noHost,
      from: opts.from,
      to: opts.to,
      body_like: opts.bodyLike,
      dry_run: opts.dryRun,
    }),
  });
}

// --- TV/Kiosk (auth só por token na URL, sem login) ---
// Sentinela de dashboard_uid que marca uma TV como Health Wall (deve casar com
// tv.WallUID no backend). Uma TV com este uid renderiza a wall em vez de um dashboard.
export const WALL_UID = "__wall__";

export interface TVResolve {
  name: string;
  dashboard?: Dashboard;
  playlist?: { name: string; screens: { dashboard: Dashboard; duration_seconds: number }[] };
  wall?: boolean; // true = TV do tipo Health Wall (consome tvWall em vez de dashboard)
  reload_at?: string | null;
}

export async function tvResolve(token: string, version = ""): Promise<TVResolve> {
  const res = await fetch(`/api/tv/resolve?token=${encodeURIComponent(token)}&v=${encodeURIComponent(version)}`);
  if (!res.ok) throw new Error(String(res.status));
  return res.json();
}

// tvWall busca os cartões da Health Wall autenticado só pelo token da TV (kiosk).
export async function tvWall(token: string): Promise<{ cards: HealthCard[] }> {
  const res = await fetch(`/api/tv/wall?token=${encodeURIComponent(token)}`);
  if (!res.ok) throw new Error(String(res.status));
  return res.json();
}

export interface TVTakeover {
  rule: string;
  /** Métrica da regra, para a TV escrever o valor com unidade. */
  metric?: string;
  severity: string;
  since: string;
  value: number;
  labels: Record<string, string>;
  diagnosis: string;
  oncall: string;
}
// TVAlert é um alerta ativo (qualquer severidade) para a TV. `id` é estável por
// alerta ativo (fingerprint), para a TV detectar quando um alerta é NOVO.
export interface TVAlert {
  id: string;
  rule: string;
  metric?: string;
  host: string;
  severity: "info" | "warning" | "critical" | string;
  since: string;
  value?: number;
}
export interface TVStatus {
  criticals: TVTakeover[];
  warnings: TVTakeover[];
  deploys: { ts: string; title: string }[];
  // Todos os alertas ativos, qualquer severidade (o backend não descarta mais `info`).
  alerts?: TVAlert[];
}
export async function tvStatus(token: string): Promise<TVStatus> {
  const res = await fetch(`/api/tv/status?token=${encodeURIComponent(token)}`);
  if (!res.ok) throw new Error(String(res.status));
  return res.json();
}

export async function tvQuery(token: string, body: object): Promise<QueryResponse> {
  const res = await fetch(`/api/tv/query?token=${encodeURIComponent(token)}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(String(res.status));
  return res.json();
}

// --- Console admin de TVs ---
export interface TVTokenInfo {
  id: number;
  name: string;
  location: string;
  dashboard_uid: string;
  playlist_id: number | null;
  revoked: boolean;
  last_seen: string;
  reload_at: string | null;
  /** Valor em claro do token (para montar o link). "" em tokens legados. */
  token: string;
}

export function tvTokens(): Promise<{ tokens: TVTokenInfo[] }> {
  return api("/api/tv/tokens");
}

export function tvCreateToken(name: string, location: string, dashboard_uid: string): Promise<{ url: string }> {
  return api("/api/tv/tokens", { method: "POST", body: JSON.stringify({ name, location, dashboard_uid }) });
}

export function tvForceReload(id: number): Promise<void> {
  return api("/api/tv/tokens/reload", { method: "POST", body: JSON.stringify({ id }) });
}

// Rotaciona o token de uma TV e devolve o novo link (o antigo deixa de valer).
// Usado para recuperar o link de TVs legadas, cujo valor em claro não foi guardado.
export function tvRegenerate(id: number): Promise<{ token: string; url: string }> {
  return api("/api/tv/tokens/regenerate", { method: "POST", body: JSON.stringify({ id }) });
}

export function tvRevoke(id: number): Promise<void> {
  return api("/api/tv/tokens/revoke", { method: "POST", body: JSON.stringify({ id }) });
}
export function tvDeleteToken(id: number): Promise<void> {
  return api(`/api/tv/tokens/${id}`, { method: "DELETE" });
}

// ---- TV playlists ----
export interface PlaylistItem { dashboard_uid: string; duration_seconds: number }
export interface Playlist { id: number; name: string; items: PlaylistItem[] }
export function tvListPlaylists(): Promise<{ playlists: Playlist[] }> {
  return api("/api/tv/playlists");
}
export function tvCreatePlaylist(p: { name: string; items: PlaylistItem[] }): Promise<{ id: number }> {
  return api("/api/tv/playlists", { method: "POST", body: JSON.stringify(p) });
}
export function tvDeletePlaylist(id: number): Promise<void> {
  return api(`/api/tv/playlists/${id}`, { method: "DELETE" });
}
export function tvSetPlaylist(id: number, playlist_id: number): Promise<void> {
  return api("/api/tv/tokens/set-playlist", { method: "POST", body: JSON.stringify({ id, playlist_id }) });
}

// ---- Usuários & Acessos (admin) ----
export interface UserAdmin {
  id: number;
  username: string; // identificador de login (para novos usuários = email)
  full_name: string;
  email: string;
  phone: string;
  role: string; // admin | operador | leitor
  disabled: boolean;
  must_reset_password: boolean;
  mfa_ativo?: boolean;
  host_count: number;
  personal_channels: string[];
}
export interface NewUserInput {
  full_name: string;
  email: string;
  phone: string;
  password: string;
  role: string; // admin | user
}
export interface ServerPerm {
  hostname: string;
  can_view: boolean;
  can_edit: boolean;
  notify: boolean;
}
export interface UserGroupLink {
  group_id: number;
  can_edit: boolean;
  notify: boolean;
}
export interface ServerGroup {
  id: number;
  name: string;
  hosts: string[];
}

export function listUsers(): Promise<{ users: UserAdmin[]; hosts_without_permission: string[] }> {
  return api("/api/users");
}
export function createUser(input: NewUserInput): Promise<{ id: number; email: string; channel_id: number }> {
  return api("/api/users", { method: "POST", body: JSON.stringify(input) });
}
export function patchUser(
  id: number,
  patch: { role?: string; disabled?: boolean; password?: string; reset_mfa?: boolean },
): Promise<void> {
  return api(`/api/users/${id}`, { method: "PATCH", body: JSON.stringify(patch) });
}
export function deleteUser(id: number): Promise<void> {
  return api(`/api/users/${id}`, { method: "DELETE" });
}
export function getUserPerms(
  id: number,
): Promise<{ direct: ServerPerm[]; groups: UserGroupLink[]; effective: ServerPerm[] }> {
  return api(`/api/users/${id}/perms`);
}
export function setUserPerms(id: number, direct: ServerPerm[], groups: UserGroupLink[]): Promise<void> {
  return api(`/api/users/${id}/perms`, { method: "PUT", body: JSON.stringify({ direct, groups }) });
}
export function listServerGroups(): Promise<{ groups: ServerGroup[] }> {
  return api("/api/server-groups");
}
export function createServerGroup(name: string, hosts: string[]): Promise<{ id: number }> {
  return api("/api/server-groups", { method: "POST", body: JSON.stringify({ name, hosts }) });
}
export function updateServerGroup(id: number, patch: { name?: string; hosts?: string[] }): Promise<void> {
  return api(`/api/server-groups/${id}`, { method: "PUT", body: JSON.stringify(patch) });
}
export function deleteServerGroup(id: number): Promise<void> {
  return api(`/api/server-groups/${id}`, { method: "DELETE" });
}

// --- Trilha de auditoria (tela Auditoria, admin-only) ---

// Uma alteração registrada: quem fez, quando, em quê e com qual conteúdo.
// `payload` é o corpo enviado com os segredos já redigidos pelo backend.
export interface AuditEntry {
  id: number;
  actor_id?: number;
  actor_name: string;
  actor_role: string;
  method: string;
  path: string;
  resource: string;
  target: string;
  status: number;
  payload: Record<string, unknown>;
  ip: string;
  created_at: string; // RFC3339
}

export interface AuditFilter {
  actor?: string;
  resource?: string;
  method?: string;
  from?: string; // RFC3339
  to?: string; // RFC3339
  limit?: number;
  offset?: number;
}

export function listAudit(
  f: AuditFilter = {},
): Promise<{ entries: AuditEntry[]; total: number; resources: string[] }> {
  const p = new URLSearchParams();
  if (f.actor) p.set("actor", f.actor);
  if (f.resource) p.set("resource", f.resource);
  if (f.method) p.set("method", f.method);
  if (f.from) p.set("from", f.from);
  if (f.to) p.set("to", f.to);
  if (f.limit) p.set("limit", String(f.limit));
  if (f.offset) p.set("offset", String(f.offset));
  const qs = p.toString();
  return api(`/api/audit${qs ? `?${qs}` : ""}`);
}

// ---------------------------------------------------------------- canal com os agentes (ARQUITETURA §7)

export type PresencaAgente = "online" | "instavel" | "offline" | "nunca_conectou";

export interface Agente {
  id: string;
  rotulo: string;
  hostname: string;
  so: string;
  arch: string;
  versao: string;
  cert_valido_ate: string;
  revogado: boolean;
  estado: PresencaAgente;
  capacidades: string[];
  visto_em?: string;
  criado_em: string;
}

export interface TokenAgente {
  token: string;
  expira_em: string;
  endereco: string;
  comandos: { linux_macos: string; windows: string };
}

export type EstadoTarefa =
  | "na_fila"
  | "enviada"
  | "executando"
  | "pausada"
  | "sucesso"
  | "falha"
  | "cancelada"
  | "recusada";

export interface Tarefa {
  id: string;
  tipo: string;
  agente_id: string;
  estado: EstadoTarefa;
  especificacao: unknown;
  progresso: number;
  iniciada_por: string;
  origem: string;
  correlacao_id: string;
  expira_em: string;
  criada_em: string;
  inicio?: string;
  fim?: string;
  erro?: string;
  resumo?: unknown;
}

export interface EventoTarefa {
  tarefa_id: string;
  seq: number;
  em: string;
  etapa: string;
  nivel: "info" | "aviso" | "erro";
  mensagem: string;
  progresso: number;
  metricas?: Record<string, number>;
}

export const listarAgentes = () => api<Agente[]>("/api/agentes");
export const criarTokenAgente = (rotulo: string, validadeHoras = 24) =>
  api<TokenAgente>("/api/agentes/tokens", { method: "POST", body: JSON.stringify({ rotulo, validade_horas: validadeHoras }) });
export const revogarAgente = (id: string) => api<void>(`/api/agentes/${encodeURIComponent(id)}/revogar`, { method: "POST" });
export const listarTarefas = (agenteId = "") =>
  api<Tarefa[]>(`/api/tarefas${agenteId ? `?agente_id=${encodeURIComponent(agenteId)}` : ""}`);
export const criarDiagnostico = (agenteId: string, especificacao: Record<string, unknown>) =>
  api<Tarefa>("/api/tarefas/diagnostico", {
    method: "POST",
    body: JSON.stringify({ agente_id: agenteId, tipo: "diagnostico.eco", especificacao }),
  });
export const controlarTarefa = (id: string, acao: "pausar" | "retomar" | "cancelar") =>
  api<Tarefa>(`/api/tarefas/${encodeURIComponent(id)}/controle`, { method: "POST", body: JSON.stringify({ acao }) });

// acompanharTarefa recebe o estado e os eventos de uma tarefa ao vivo (SSE). O
// servidor manda o histórico primeiro e encerra o fluxo quando a tarefa termina.
export function acompanharTarefa(
  id: string,
  aoReceber: (a: { tarefa?: Tarefa; evento?: EventoTarefa }) => void,
  signal?: AbortSignal,
): Promise<void> {
  return postSSE<Tarefa | EventoTarefa>(
    `/api/tarefas/${encodeURIComponent(id)}/ao-vivo`,
    {},
    (d) => aoReceber("seq" in d ? { evento: d } : { tarefa: d }),
    signal,
  );
}

// ---------------------------------------------------------------- migração de dados (ARQUITETURA §9)

export type Motor = "firebird" | "postgres";

export interface ConexaoBanco {
  id: string;
  nome: string;
  motor: Motor;
  endereco: string;
  banco: string;
  usuario: string;
  opcoes: Record<string, string> | null;
  agente_preferido_id?: string;
  versao_detectada: string;
  criada_por: string;
  criada_em: string;
}

export interface NovaConexao {
  nome: string;
  motor: Motor;
  endereco: string;
  banco: string;
  usuario: string;
  senha: string;
  opcoes?: Record<string, string>;
  agente_preferido_id?: string;
}

export type TipoLogico =
  | "inteiro" | "decimal" | "flutuante" | "texto" | "texto_longo" | "data" | "hora"
  | "data_hora" | "booleano" | "binario" | "json" | "uuid" | "desconhecido";

export interface ColunaEsquema {
  nome: string;
  tipo_nativo: string;
  tipo: TipoLogico;
  tamanho?: number;
  precisao?: number;
  escala?: number;
  nulavel: boolean;
  padrao?: string;
  charset?: string;
  identidade?: boolean;
}

export interface TabelaEsquema {
  nome: string;
  colunas: ColunaEsquema[];
  chave_primaria?: string[];
  estrangeiras?: { nome: string; colunas: string[]; tabela_ref: string; colunas_ref: string[] }[];
  indices?: { nome: string; colunas: string[]; unico: boolean }[];
  linhas_estimadas: number;
}

export interface Esquema {
  motor: Motor;
  versao: string;
  charset: string;
  dialeto?: number;
  tabelas: TabelaEsquema[];
  capturado_em: string;
}

export type Transformacao =
  | "nenhuma" | "converter_tipo" | "charset" | "aparar" | "valor_padrao" | "constante"
  | "mapa_valores" | "concatenar" | "dividir" | "data_formato";

export interface MapColuna {
  coluna_origem?: string;
  coluna_destino: string;
  transformacao: Transformacao;
  parametros?: Record<string, string>;
  confianca?: number;
  origem?: "heuristica" | "mcp" | "usuario";
  alerta?: string;
}

export interface MapTabela {
  tabela_origem: string;
  tabela_destino?: string;
  acao: "copiar" | "ignorar" | "criar_no_destino";
  ordem: number;
  coluna_lote?: string;
  filtros?: { coluna: string; operador: string; valor?: string }[];
  colunas: MapColuna[];
  confianca?: number;
  origem?: string;
}

export interface Mapeamento {
  tabelas: MapTabela[];
}

export interface Problema {
  nivel: "erro" | "aviso";
  tabela?: string;
  coluna?: string;
  mensagem: string;
}

export interface VersaoMapeamento {
  projeto_id: string;
  versao: number;
  origem: string;
  conteudo: Mapeamento;
  hash: string;
  problemas: Problema[];
  estado: "rascunho" | "valido" | "aprovado";
  criado_por: string;
  criado_em: string;
  aprovado_por?: string;
  aprovado_em?: string;
}

export interface ProjetoMigracao {
  id: string;
  nome: string;
  tipo: "troca_de_banco" | "upgrade_versao";
  origem_id: string;
  destino_id?: string;
  estado: string;
  criado_por: string;
  criado_em: string;
}

export const listarConexoes = () => api<ConexaoBanco[]>("/api/migracao/conexoes");
export const criarConexao = (c: NovaConexao) =>
  api<ConexaoBanco>("/api/migracao/conexoes", { method: "POST", body: JSON.stringify(c) });
export const apagarConexao = (id: string) => api<void>(`/api/migracao/conexoes/${encodeURIComponent(id)}`, { method: "DELETE" });
export const capturarEsquema = (id: string, agenteId: string) =>
  api<{ id: string; hash: string; versao: string; charset: string; tabelas: number }>(
    `/api/migracao/conexoes/${encodeURIComponent(id)}/capturar`,
    { method: "POST", body: JSON.stringify({ agente_id: agenteId }) },
  );
export const ultimoEsquema = (id: string) =>
  api<{ id: string; hash: string; conteudo: Esquema; capturado_em: string }>(`/api/migracao/conexoes/${encodeURIComponent(id)}/esquema`);
export const listarProjetos = () => api<ProjetoMigracao[]>("/api/migracao/projetos");
export const criarProjeto = (p: { nome: string; tipo: string; origem_id: string; destino_id?: string }) =>
  api<ProjetoMigracao>("/api/migracao/projetos", { method: "POST", body: JSON.stringify(p) });
export const obterProjeto = (id: string) =>
  api<{ projeto: ProjetoMigracao; mapeamento?: VersaoMapeamento }>(`/api/migracao/projetos/${encodeURIComponent(id)}`);
export const sugerirMapeamento = (id: string) =>
  api<VersaoMapeamento>(`/api/migracao/projetos/${encodeURIComponent(id)}/sugerir`, { method: "POST" });
export const salvarMapeamento = (id: string, mapeamento: Mapeamento) =>
  api<VersaoMapeamento>(`/api/migracao/projetos/${encodeURIComponent(id)}/mapeamento`, {
    method: "PUT",
    body: JSON.stringify({ mapeamento, origem: "usuario" }),
  });
export const aprovarMapeamento = (id: string, versao: number) =>
  api<VersaoMapeamento>(`/api/migracao/projetos/${encodeURIComponent(id)}/aprovar`, {
    method: "POST",
    body: JSON.stringify({ versao }),
  });

// ---------------------------------------------------------------- execução (ARQUITETURA §9.4)

export interface AmostraProblema {
  chave?: string;
  coluna?: string;
  tipo: string;
  mensagem: string;
}

export interface RelatorioTabela {
  origem: string;
  destino: string;
  acao: string;
  estrategia: "apagar_tabela" | "esvaziar" | "staging";
  linhas: number;
  linhas_ok: number;
  com_problema: number;
  violacoes?: Record<string, number>;
  perdas?: Record<string, number>;
  amostras?: AmostraProblema[];
  duracao_ms: number;
}

export interface RelatorioSimulacao {
  hash_mapeamento: string;
  ordem: string[];
  ciclos?: string[][];
  tabelas: RelatorioTabela[];
  total_linhas: number;
  bloqueantes: number;
  perdas: number;
  tempo_estimado_s: number;
  duracao_ms: number;
}

// Conferência de conteúdo (ARQUITETURA §9.4): por tabela, além da contagem e da soma das
// chaves, a soma do conteúdo de cada coluna entre o gravado e o relido do destino.
// Nunca traz valores de linha — só a chave (para quem pode executar) e o nome das colunas.
export interface ColunaNaoConferida {
  coluna: string;
  motivo: string;
}

export interface DivergenciaConteudo {
  chave?: string; // vazia para quem não pode executar migração
  colunas?: string[];
  ausente?: boolean;
}

export interface ResumoTabelaExecucao {
  origem: string;
  destino: string;
  linhas: number;
  gravadas: number;
  checksum_ok: boolean;
  conteudo_ok?: boolean; // ausente = resumo de um agente que não conferia conteúdo
  conteudo_motivo?: string;
  colunas_conferidas?: string[];
  colunas_nao_conferidas?: ColunaNaoConferida[];
  divergencias?: DivergenciaConteudo[];
  duracao_ms: number;
}

export interface ResumoVerificacaoTabela {
  origem: string;
  destino: string;
  onde: "tabela" | "staging";
  linhas: number;
  identicas: number;
  divergentes: number;
  ausentes: number;
  nao_localizadas?: number; // chave gerada pelo destino ou repetida lá: não deu para achar
  por_coluna?: Record<string, number>;
  colunas_conferidas?: string[];
  colunas_nao_conferidas?: ColunaNaoConferida[];
  divergencias?: DivergenciaConteudo[];
  sem_chave?: boolean;
  conteudo_ok: boolean;
  motivo?: string;
  texto?: ConferenciaTexto; // ausente = resultado de um agente sem a dupla conferência
  duracao_ms: number;
}

// Dupla conferência pelo texto dos próprios bancos: cada banco renderiza o valor como
// texto no servidor (independente do driver e da conversão do agente).
export interface ConferenciaTexto {
  ok: boolean;
  linhas: number;
  identicas: number;
  divergentes: number;
  ausentes: number;
  por_coluna?: Record<string, number>;
  colunas?: string[];
  parciais?: ColunaNaoConferida[];
  nao_conferidas?: ColunaNaoConferida[];
  divergencias?: DivergenciaConteudo[];
  motivo?: string;
}

export interface ResumoVerificacao {
  execucao: string;
  fase: string;
  tabelas: ResumoVerificacaoTabela[];
  linhas: number;
  identicas: number;
  divergentes: number;
  ausentes: number;
  conteudo_ok: boolean;
  texto_ok?: boolean;
  colunas_texto?: number;
  verificado_em: string;
  duracao_ms: number;
}

export interface ResumoExecucao {
  execucao: string;
  tabelas: ResumoTabelaExecucao[];
  linhas: number;
  manifesto: { fase: string; itens: { tabela: string; estrategia: string; trocada?: boolean }[] };
  avisos?: string[];
  duracao_ms: number;
}

export interface ExecucaoMigracao {
  tarefa_id: string;
  projeto_id: string;
  versao: number;
  tipo: "simular" | "executar" | "reverter" | "verificar";
  execucao: string;
  hash_mapeamento: string;
  relacionada_id?: string;
  criada_em: string;
  tarefa: Tarefa;
}

export interface SituacaoExecucoes {
  execucoes: ExecucaoMigracao[];
  ocupado: boolean;
  pode_executar: boolean;
  incompletas: Record<string, string>; // execucao → tarefa a retomar
  revertidas: Record<string, boolean>;
  concluidas: Record<string, boolean>;
  versao_aprovada?: number;
  simulacao_ok?: boolean;
  simulacao?: RelatorioSimulacao;
}

const pj = (id: string) => `/api/migracao/projetos/${encodeURIComponent(id)}`;
export const execucoesProjeto = (id: string) => api<SituacaoExecucoes>(`${pj(id)}/execucoes`);
export const simularMigracao = (id: string, agenteId: string, versao?: number) =>
  api<ExecucaoMigracao>(`${pj(id)}/simular`, { method: "POST", body: JSON.stringify({ agente_id: agenteId, versao }) });
export const executarMigracao = (id: string, agenteId: string, retomarDe?: string) =>
  api<ExecucaoMigracao>(`${pj(id)}/executar`, { method: "POST", body: JSON.stringify({ agente_id: agenteId, retomar_de: retomarDe }) });
export const reverterMigracao = (id: string, execucaoId: string) =>
  api<ExecucaoMigracao>(`${pj(id)}/reverter`, { method: "POST", body: JSON.stringify({ execucao_id: execucaoId }) });
// Verificar de novo: compara origem e destino linha a linha (só leitura).
export const verificarMigracao = (id: string, execucaoId: string, agenteId?: string) =>
  api<ExecucaoMigracao>(`${pj(id)}/verificar`, {
    method: "POST",
    body: JSON.stringify({ execucao_id: execucaoId, agente_id: agenteId }),
  });

// ---------------------------------------------------------------- upgrade Firebird → 5 (ARQUITETURA §9.5)

export interface AchadoUpgrade {
  nivel: "bloqueio" | "risco" | "info";
  categoria: string;
  objeto?: string;
  mensagem: string;
  quantos?: number;
  correcao?: string;
}

export interface DiagnosticoUpgrade {
  versao: string;
  ods: string;
  dialeto: number;
  charset: string;
  page_size: number;
  tamanho_bytes: number;
  tabelas: number;
  procedures: number;
  triggers: number;
  udfs: number;
  achados: AchadoUpgrade[];
  ensaio?: { feito: boolean; ok: boolean; erros?: string[]; duracao_ms: number };
  nota: number;
  risco: "baixo" | "medio" | "alto";
  bloqueios: number;
  parada_estimada_s: number;
  fix_sql: string;
  charset_fix?: string;
  capturado_em: string;
}

export interface ResumoUpgrade {
  execucao: string;
  backup: string;
  novo_banco: string;
  versao_nova: string;
  ods_nova: string;
  tabelas: { tabela: string; antes: number; depois: number; ok: boolean }[];
  geradores: { tabela: string; antes: number; depois: number; ok: boolean }[];
  tudo_confere: boolean;
  avisos?: string[];
  reverter: string;
  duracao_ms: number;
}

export interface ExecucaoUpgrade extends Omit<ExecucaoMigracao, "tipo"> {
  tipo: "diagnosticar" | "atualizar" | "descartar";
}

export interface SituacaoUpgrade {
  execucoes: ExecucaoUpgrade[];
  ocupado: boolean;
  tem_destino: boolean;
  descartados: Record<string, boolean>;
  diagnostico?: DiagnosticoUpgrade;
  pode_atualizar: boolean;
  pode_descartar?: string;
  motivo?: string;
}

export const situacaoUpgrade = (id: string) => api<SituacaoUpgrade>(`${pj(id)}/upgrade`);
export const diagnosticarUpgrade = (id: string, agenteId: string, validarPaginas: boolean) =>
  api<ExecucaoUpgrade>(`${pj(id)}/diagnosticar`, { method: "POST", body: JSON.stringify({ agente_id: agenteId, validar_paginas: validarPaginas }) });
export const atualizarFirebird = (id: string, agenteId: string, charsetFix: string) =>
  api<ExecucaoUpgrade>(`${pj(id)}/atualizar`, { method: "POST", body: JSON.stringify({ agente_id: agenteId, charset_fix: charsetFix }) });
export const descartarUpgrade = (id: string, execucaoId: string) =>
  api<ExecucaoUpgrade>(`${pj(id)}/descartar`, { method: "POST", body: JSON.stringify({ execucao_id: execucaoId }) });

// ---------------------------------------------------------------- MCP (ARQUITETURA §12)

export type EscopoMCP = "leitura" | "mapeamento" | "simulacao";

export interface TokenMCP {
  id: string;
  nome: string;
  escopos: EscopoMCP[];
  criado_por: string;
  criado_em: string;
  expira_em: string;
  ultimo_uso?: string;
  revogado: boolean;
}

export const listarTokensMCP = () => api<TokenMCP[]>("/api/mcp/tokens");
export const criarTokenMCP = (nome: string, escopos: EscopoMCP[], validadeDias: number) =>
  api<{ token: string; id: string; nome: string; escopos: EscopoMCP[]; expira_em: string }>("/api/mcp/tokens", {
    method: "POST",
    body: JSON.stringify({ nome, escopos, validade_dias: validadeDias }),
  });
export const revogarTokenMCP = (id: string) => api<void>(`/api/mcp/tokens/${encodeURIComponent(id)}/revogar`, { method: "POST" });

// ---------------------------------------------------------------- deploy (ARQUITETURA §10)

export const listarDeploys = (limite = 50) => api<Tarefa[]>(`/api/deploys?limite=${limite}`);
export const criarDeploy = (p: { agente: string; aplicacao: string; versao: string; ambiente?: string }) =>
  api<Tarefa>("/api/deploys", { method: "POST", body: JSON.stringify(p) });
