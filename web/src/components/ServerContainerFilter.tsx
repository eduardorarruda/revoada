import { useHostContainers } from "../hooks/useHostContainers";
import { useHostNames } from "../hooks/useHostNames";

// ServerContainerFilter — filtro consolidado servidor → container (encadeado) e,
// opcionalmente, serviço no MESMO grupo (regra 5.9). Ao escolher um servidor, a lista
// de containers passa a mostrar só os daquele servidor. O serviço (quando `services` é
// informado) é um filtro independente exibido junto, para centralizar tudo num único
// controle. Emite os valores técnicos (hostname, container, serviço) para a consulta e
// exibe o nome amigável do servidor. Sem servidor, o seletor de container fica desabilitado.
export function ServerContainerFilter({
  host,
  container,
  onChange,
  allHostsLabel = "Todos os servidores",
  allContainersLabel = "Todos os containers",
  services,
  service = "",
  onServiceChange,
  allServicesLabel = "todos os serviços",
}: {
  host: string;
  container: string;
  onChange: (next: { host: string; container: string }) => void;
  allHostsLabel?: string;
  allContainersLabel?: string;
  services?: string[];
  service?: string;
  onServiceChange?: (service: string) => void;
  allServicesLabel?: string;
}) {
  const { hosts, containersOf } = useHostContainers();
  const { hostLabel } = useHostNames();
  const containers = containersOf(host);

  return (
    <div className="sc-filter">
      <select
        value={host}
        onChange={(e) => onChange({ host: e.target.value, container: "" })}
        aria-label="Servidor"
      >
        <option value="">{allHostsLabel}</option>
        {hosts.map((h) => (
          <option key={h} value={h}>
            {hostLabel(h)}
          </option>
        ))}
      </select>
      <select
        value={container}
        onChange={(e) => onChange({ host, container: e.target.value })}
        disabled={!host || containers.length === 0}
        aria-label="Container"
        title={!host ? "Escolha um servidor primeiro" : containers.length === 0 ? "Sem containers neste servidor" : undefined}
      >
        <option value="">{allContainersLabel}</option>
        {containers.map((c) => (
          <option key={c} value={c}>
            {c}
          </option>
        ))}
      </select>
      {services && onServiceChange && (
        <select
          value={service}
          onChange={(e) => onServiceChange(e.target.value)}
          aria-label="Serviço"
        >
          <option value="">{allServicesLabel}</option>
          {services.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
      )}
    </div>
  );
}
