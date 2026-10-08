#!/usr/bin/env python3
"""Validação de ponta a ponta do Revoada contra uma stack viva.

Prova que o número da tela é a medida real: compara o que o painel mostra (pelas
MESMAS rotas que o front chama) com a verdade lida direto do kernel e com o que
um servidor de teste efetivamente recebeu. Ver README.md.

Só biblioteca padrão do Python 3. Nenhuma credencial fica no código.
"""
import argparse
import os
import socket
import sys
import tempfile
import threading
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import cliente  # noqa: E402
import grupo_host  # noqa: E402
import grupo_ia  # noqa: E402
import grupo_logs  # noqa: E402
import grupo_sites  # noqa: E402
import relatorio  # noqa: E402
import servidor_teste  # noqa: E402

GRUPOS = ("host", "logs", "sites", "ia")


def args_cli():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--painel", default=os.environ.get("RV_URL", "http://127.0.0.1:8091"), help="URL do painel")
    p.add_argument("--dev", help="diretório de dev com senha-admin.txt e totp-admin.txt")
    p.add_argument("--email", default="admin@exemplo.com", help="usuário admin (ou RV_EMAIL)")
    p.add_argument("--senha-arquivo", help="arquivo com a senha (ou RV_SENHA)")
    p.add_argument("--totp-arquivo", help="arquivo com o segredo TOTP (ou RV_TOTP)")
    p.add_argument("--host", default=socket.gethostname(), help="hostname do agente local (padrão: este host)")
    p.add_argument("--dir-logs", help="diretório coberto por log_paths do agente (ex.: <dev>/logs)")
    p.add_argument("--dir-disco", default=os.path.expanduser("~/.cache/revoada-validacao"),
                   help="diretório num disco MONITORADO para o teste de escrita de 2 GiB (não use tmpfs)")
    p.add_argument("--relatorio", help="caminho-base do relatório (sem extensão); padrão: diretório temporário")
    p.add_argument("--somente", default=",".join(GRUPOS), help="grupos a rodar: " + ",".join(GRUPOS))
    p.add_argument("--duracao-host", type=int, default=330, help="segundos de amostragem do host (≥300 cobre o cartão de rede)")
    p.add_argument("--duracao-sites", type=int, default=230, help="segundos observando os checks de site")
    p.add_argument("--sem-carga", action="store_true", help="não gera carga de CPU")
    p.add_argument("--sem-disco", action="store_true", help="não escreve o arquivo de 2 GiB")
    p.add_argument("--sonda", help="probe_location de um agente em modo sonda já rodando (opcional)")
    p.add_argument("--gateway", default=os.environ.get("RV_GATEWAY", "http://127.0.0.1:8090"),
                   help="URL do gateway (grupo ia envia spans OTLP para ele)")
    p.add_argument("--chave-ingestao", default=os.environ.get("RV_CHAVE"),
                   help="chave de ingestão de um servidor (X-Revoada-Key) para o grupo ia (ou RV_CHAVE)")
    p.add_argument("--duracao-ia", type=int, default=480,
                   help="segundos esperando a série llm.* (o runner fecha cada minuto com 5 min de atraso)")
    a = p.parse_args()
    a.saida_dir = os.path.dirname(a.relatorio) if a.relatorio else tempfile.mkdtemp(prefix="revoada-validacao-")
    if not a.relatorio:
        a.relatorio = os.path.join(a.saida_dir, "relatorio")
    os.makedirs(a.saida_dir, exist_ok=True)
    return a


def main():
    args = args_cli()
    grupos = [g.strip() for g in args.somente.split(",") if g.strip()]
    if "ia" in grupos and not args.chave_ingestao:
        raise SystemExit("--chave-ingestao (ou RV_CHAVE) é obrigatória para o grupo ia")
    if "logs" in grupos and not args.dir_logs:
        if args.dev and os.path.isdir(os.path.join(args.dev, "logs")):
            args.dir_logs = os.path.join(args.dev, "logs")
        else:
            raise SystemExit("--dir-logs é obrigatório para o grupo logs")
    run = time.strftime("%H%M%S")
    painel = cliente.Painel(args.painel, cliente.Credenciais.de_args(args)).entrar()
    hosts = {h["hostname"] for h in painel.get("/api/hosts")["hosts"]}
    if args.host not in hosts and ({"host", "logs"} & set(grupos)):
        raise SystemExit(f"host {args.host!r} não está no inventário do painel ({sorted(hosts)})")
    res = relatorio.Resultados()
    contexto = {"inicio": time.strftime("%Y-%m-%d %H:%M:%S %z"), "painel": args.painel, "host": args.host,
                "grupos": ",".join(grupos), "execucao": run, "nucleos": os.cpu_count()}
    srv = None
    threads = []
    try:
        if "sites" in grupos:
            srv = servidor_teste.ServidorTeste(f"val{run}", dir_tmp=args.saida_dir)
            threads.append(threading.Thread(target=grupo_sites.executar, args=(res, painel, srv, args, run)))
        if "logs" in grupos:
            # cada grupo usa a própria sessão: o refresh de token de um não derruba o outro
            p2 = cliente.Painel(args.painel, painel.cred)
            p2.jar, p2.token = painel.jar, painel.token
            threads.append(threading.Thread(target=grupo_logs.executar, args=(res, p2, args.host, args, run)))
        if "ia" in grupos:
            p3 = cliente.Painel(args.painel, painel.cred)
            p3.jar, p3.token = painel.jar, painel.token
            threads.append(threading.Thread(target=grupo_ia.executar, args=(res, p3, args, run)))
        if "host" in grupos:
            threads.append(threading.Thread(target=grupo_host.executar, args=(res, painel, args.host, args)))
        for t in threads:
            t.start()
        for t in threads:
            t.join()
    finally:
        if srv:
            srv.parar()
    contexto["fim"] = time.strftime("%Y-%m-%d %H:%M:%S %z")
    print(relatorio.tabela_terminal(res))
    j, m = relatorio.gravar(res, args.relatorio, contexto)
    print(f"\nrelatório: {j}\n           {m}")
    return 1 if res.resumo()[relatorio.FALHOU] else 0


if __name__ == "__main__":
    sys.exit(main())
