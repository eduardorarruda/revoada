# Schema do modelo de dashboard

O modelo é um JSON versionado (tabela `dashboards.model`, histórico em `dashboard_versions`).
Cada `UPDATE` incrementa `version` e grava a versão anterior; `rollback` restaura um modelo
antigo como uma nova versão (o histórico nunca é perdido).

## Estrutura
```json
{
  "uid": "host-web01",
  "title": "Visão do Host — web01",
  "variables": [{ "name": "host", "current": "web01" }],
  "timeRange": { "from": "now-6h", "to": "now" },
  "panels": [
    {
      "id": 1,
      "type": "timeseries",
      "title": "CPU (%)",
      "description": "Uso de CPU. Normal < 70%.",
      "gridPos": { "x": 0, "y": 0, "w": 12, "h": 8 },
      "query": { "metric": "system.cpu.utilization", "filters": { "host": "web01" }, "agg": "avg" }
    }
  ]
}
```

## Campos
| Campo | Descrição |
|---|---|
| `uid` | identificador único e estável (usado na URL) |
| `title` / `folder` | nome e pasta (a pasta é coluna própria, não fica no modelo) |
| `variables[]` | variáveis do dashboard (`name`, `current`); referenciáveis em filtros como `$name` |
| `timeRange` | janela padrão (`from`/`to`, formato `now-6h` / `now`) |
| `panels[]` | painéis (ver abaixo) |

### panel
| Campo | Descrição |
|---|---|
| `id` | único no dashboard |
| `type` | `timeseries` \| `stat` \| `gauge` \| `bargauge` \| `table` \| `state-timeline` \| `heatmap` \| `text` |
| `title` / `description` | título e texto explicativo (princípio Revoada) |
| `gridPos` | posição na grade de **24 colunas** (`x`,`y`,`w`,`h`) |
| `query` | `{metric, filters, agg, group_by?}` — enviado à Query API; a tabela (bruto/1m/1h) é escolhida pela janela/step |
| `query.group_by` | opcional: lista de labels para agrupar (ex.: `["host"]` → uma série por servidor). Sem `group_by`, o painel agrega tudo numa série. Painéis `stat`/`gauge` com mais de uma série caem para `bargauge` (não escondem hosts). |

Exemplo de painel multi-servidor (CPU de cada host numa série):
```json
{ "type": "timeseries", "title": "CPU por servidor",
  "query": { "metric": "system.cpu.utilization", "agg": "avg", "group_by": ["host"] } }
```

## API
| Método | Rota | Papel |
|---|---|---|
| GET | `/api/dashboards?search=` | listar (qualquer autenticado) |
| GET | `/api/dashboards/{uid}` | obter |
| GET | `/api/dashboards/{uid}/versions` | histórico |
| POST | `/api/dashboards` | criar (editor+) |
| PUT | `/api/dashboards/{uid}` | salvar nova versão (editor+) |
| DELETE | `/api/dashboards/{uid}` | remover (editor+) |
| POST | `/api/dashboards/{uid}/rollback` `{version}` | restaurar versão (editor+) |
| POST | `/api/dashboards/starter?host=` | gerar "Visão do Host" (editor+) |
| GET | `/api/hosts` | hosts ativos (inventário / starter) |

## Starter pack "Visão do Host"
Gerado por `internal/dashboards/StarterHost(host)`: 6 painéis (CPU, Memória, Disco por mount,
Rede recebida, Uptime, Conexões) já filtrados pelo host.
