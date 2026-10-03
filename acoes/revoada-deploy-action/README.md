# Revoada Deploy (GitHub Action)

Implanta uma aplicação num servidor **pelo agente do Revoada que já está nele** — sem runner
self-hosted, sem SSH aberto para o GitHub. O agente roda a receita que o dono do servidor
configurou, confere o health check e **volta sozinho para a versão anterior** se falhar. Cada deploy
vira anotação nos gráficos do servidor.

```yaml
- uses: eduardorarruda/revoada-deploy-action@v1
  with:
    painel: https://painel.suaempresa.com.br
    token: ${{ secrets.REVOADA_DEPLOY_TOKEN }}
    agente: srv-loja-01
    aplicacao: loja
    versao: ${{ github.ref_name }}
    ambiente: producao
```

No servidor (`agent.yaml`), o dono decide o que pode ser implantado — o painel só manda nome e versão:

```yaml
canal:
  tarefas_permitidas: [deploy.aplicar]
  deploy:
    loja:
      tipo: compose            # docker compose pull + up -d com REVOADA_VERSAO=<versão>
      diretorio: /srv/loja
      saude: http://127.0.0.1:8080/healthz
    api:
      tipo: script             # .sh no Linux/macOS, .ps1 no Windows
      diretorio: C:\apps\api
      script: deploy.ps1       # recebe REVOADA_VERSAO
      rollback: rollback.ps1   # recebe REVOADA_VERSAO (a que falhou) e REVOADA_VERSAO_ANTERIOR
      saude: http://127.0.0.1:9000/health
```

Saídas: `deploy-id` e `estado` (`sucesso` · `falha` · `cancelada` · `recusada`). O resumo do job
mostra a saída do deploy e se houve rollback.
