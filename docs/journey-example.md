# Jornadas de browser

Uma jornada é um roteiro multi-passo que valida um fluxo real (não só "a página
responde 200"), como o envio de um formulário de landing page. Detecta form
quebrado que o check HTTP simples não pega.

Crie via `POST /api/journeys` (editor+). Exemplo — abrir a LP, enviar o form e
conferir a mensagem de sucesso:

```json
{
  "name": "Contato LP",
  "interval_seconds": 300,
  "steps": [
    {
      "name": "abrir formulário",
      "method": "GET",
      "url": "https://lp.exemplo.com.br/contato",
      "assert_contains": "Fale conosco"
    },
    {
      "name": "enviar formulário",
      "method": "POST",
      "url": "https://lp.exemplo.com.br/contato/enviar",
      "form": { "nome": "Teste", "email": "#####", "mensagem": "ping" },
      "assert_status": 200,
      "assert_contains": "Recebemos sua mensagem"
    }
  ]
}
```

Campos de cada passo:
- `method`: `GET` (default) ou `POST`.
- `url`: alvo do passo.
- `form`: campos enviados como `application/x-www-form-urlencoded` (POST).
- `assert_status`: status HTTP esperado (opcional; sem ele, ≥400 falha).
- `assert_contains`: texto que deve aparecer no corpo (opcional).

Os passos compartilham um cookie jar (a sessão do passo 1 vale no passo 2). A falha
do primeiro passo aborta a jornada com o diagnóstico (`passo N (nome): motivo`).

Estado e alertas seguem o padrão dos checks: 1ª falha = SUSPEITO (reteste em 30s),
2ª consecutiva = DOWN e notifica; recuperação resolve. Métricas gravadas:
`synthetic.journey.up` e `synthetic.journey.total_ms` (label `journey`).

## Limitação honesta

Esta versão executa passos **HTTP** (com cookies), cobrindo formulários
server-side. Jornadas que dependem de JavaScript pesado no browser (SPAs, widgets)
precisam de um runner headless (Playwright) — fica registrado como follow-up; a
estrutura de passos/estado/alerta já está pronta para recebê-lo.
