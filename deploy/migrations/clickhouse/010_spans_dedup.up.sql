-- O batcher do gateway reenvia o MESMO bloco quando um insert falha no meio do caminho
-- (chbatch: retry com os mesmos rows). genai_spans já nasceu deduplicando (009); spans
-- não, e um retry duplicava spans no waterfall e inflava contagem e duração do trace.
-- Dois lotes genuinamente idênticos não existem (span_id é único), então a janela só
-- descarta o reenvio.
ALTER TABLE spans MODIFY SETTING non_replicated_deduplication_window = 1000;
