# Segurança

## Como relatar uma vulnerabilidade

Não abra issue pública. Use o relato privado do GitHub:
**Security → Report a vulnerability** neste repositório.

Inclua o que for possível: versão/commit, passos para reproduzir, impacto e, se tiver, uma
sugestão de correção. A resposta inicial sai em até 7 dias; a correção e o aviso público são
combinados com quem relatou.

## O que está no escopo

Painel (API, interface, MCP), gateway de ingestão, agente, canal mTLS entre painel e agente,
cofre de credenciais e motor de migração.

## Como o Revoada se protege

Os controles (HTTPS, Argon2id, MFA, papéis, cofre em envelope, mTLS, tarefas assinadas,
auditoria encadeada, redação de segredos nos logs…) estão descritos em
[docs/SEGURANCA.md](docs/SEGURANCA.md).
