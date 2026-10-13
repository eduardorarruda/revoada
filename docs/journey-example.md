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
      "form": { "nome": "Teste", "email": "teste@exemplo.com.br", "mensagem": "ping" },
      "assert_status": 200,
      "assert_contains": "Recebemos sua mensagem"
    }
  ]
}
```

Campos de cada passo:
- `method`: verbo HTTP do passo — `GET` (default), `POST`, `PUT`, `PATCH`, `DELETE`
  ou `HEAD` (o servidor repassa o verbo como veio, em maiúsculas).
- `url`: alvo do passo.
- `form`: campos enviados no corpo como `application/x-www-form-urlencoded`. Só vale
  em `POST`, `PUT` e `PATCH`; nos demais métodos o passo sai sem corpo e o `form`
  é ignorado.
- `assert_status`: status HTTP esperado (opcional; sem ele, ≥400 falha).
- `assert_contains`: texto que deve aparecer no corpo (opcional).

Os passos compartilham um cookie jar (a sessão do passo 1 vale no passo 2). A falha
do primeiro passo aborta a jornada com o diagnóstico (`passo N (nome): motivo`).

Estado e alertas seguem o padrão dos checks: 1ª falha = SUSPEITO (reteste em 30s),
2ª consecutiva = DOWN e notifica; recuperação resolve. Métricas gravadas:
`synthetic.journey.up` e `synthetic.journey.total_ms` (label `journey`).

## Limitação

Cada passo é uma requisição **HTTP** (com cookies), feita a partir do servidor do
painel. Isso cobre formulários processados no servidor. Jornadas com JavaScript no
navegador (SPAs) não são suportadas.
