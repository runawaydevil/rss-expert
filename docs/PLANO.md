# Plano de construção — RSS Social 0.0.1

## Contexto

[PROPOSTA.md](B:\NetShare\Projetos\rss-social\PROPOSTA.md) define o produto: um **leitor social da web aberta** — implementação limpa em Go, binário único em container `FROM scratch`, modelo de dados de 3 camadas com convergência determinística, interop RSS/Textcasting verificada por CI. Nada foi implementado; o diretório contém apenas a proposta e **não é ainda um repositório git**.

**Decisões tomadas pelo Pablo (23/07/2026):**

| Questão (§13) | Decisão |
|---|---|
| Licença | **AGPL-3.0** |
| Fatiamento | **Espinha completa** (publicar + ler + federar desde o início) |
| Visual | **Proposta nova neutra** — papel/tinta, acento único sóbrio |
| Escopo | **Roteiro completo da proposta**, mas versionado com humildade: tudo isso é a **versão 0.0.1**. Os antigos "v0.1…v0.6" viram **fases internas de construção**, não releases |

**Exigência central de design:** aparência robusta, de produto real — *não pode parecer feito por IA*. Ver §Design abaixo: regras vinculantes anti-"AI slop".

---

## Verificação da pesquisa (web, 23/07/2026)

Toda a stack da proposta foi re-verificada hoje:

| Item | Estado | Ação |
|---|---|---|
| [`ncruces/go-sqlite3`](https://github.com/ncruces/go-sqlite3) | Vivo, mas **migrou recentemente de wazero para "wasm2go"** — o próprio autor recomenda versões anteriores se você valoriza estabilidade | Na Fase 0: **pinar a última versão baseada em wazero (v0.33.2)**, medir memória (§13.4 da proposta), manter plano B `modernc.org/sqlite`. Tem `sqlite3.WithMaxMemory()` para teto por conexão — usar |
| [`a-h/templ`](https://github.com/a-h/templ) | Ativo (release 05/2026, 5.4k importadores) | Adotar |
| [HTMX](https://htmx.org/) | 2.0.9 estável (04/2026) | Adotar. **Datastar avaliado e rejeitado** (mais novo, menos maduro; SSE já temos via stdlib) — registrar como ADR-0002 |
| [`mmcdole/gofeed`](https://github.com/mmcdole/gofeed) | Ativo; `Item.Extensions` guarda namespaces desconhecidos (é o que `source:` precisa) | Adotar |
| [`go.hacdias.com/indielib`](https://github.com/hacdias/indielib) | Existe, MIT, IndieAuth client/server + Micropub + post-discovery | Validar cobertura na Fase 4 antes de adotar (como a proposta pede) |
| [threadwalker](https://github.com/scripting/rss.chat/tree/main/examples/threadwalker) | **Confirmado**: Node.js, caminha `source:comments` recursivamente, sem API, só XML estático | Rodar no CI contra nossos feeds (portão da Fase 0) |
| `goldmark`, `bluemonday`, `goose`, `willnorris/microformats`, `prometheus/client_golang` | Padrões de fato, ativos | Adotar |

---

## Design — "Editorial Instrumentado" concretizado

### Referências estudadas (o que produtos reais e respeitados fazem)

- **[Feedbin](https://feedbin.com/)** — o padrão-ouro de leitor: tipografia como recurso central (fontes Hoefler & Co.), temas claro/escuro intencionais, modo leitura. É o benchmark direto.
- **iA / iA Writer** — disciplina tipográfica radical: uma coluna, medida controlada, quase nenhuma cor. Prova que restrição = sofisticação.
- **Readwise Reader** — densidade configurável e leitura séria num web app moderno.
- **Miniflux** — o extremo espartano; referência de "funciona sem JS", não de estética.
- **[minimal.gallery](https://minimal.gallery/)** e **[SiteInspire](https://www.siteinspire.com/)** — curadoria contínua de design editorial/tipográfico durante a construção (Godly/Awwwards são o oposto do que queremos: espetáculo).
- **Berkeley Graphics** — estética de "fato do sistema": monoespaçada como voz de instrumentação, tabelas densas, zero decoração.

### Regras vinculantes anti-"cara de IA"

O que denuncia design gerado e está **proibido**:

1. **Nada de gradiente roxo/azul, glassmorphism, blobs, hero centrado com headline genérica.**
2. **Nada de emoji na interface.** Ícones: um único conjunto de traço (Lucide subsetado inline em SVG), 16px, sempre acompanhados de texto.
3. **Nada de cards com sombra para tudo.** Ritmo por divisores de 1px e espaço em branco (proposta §8.5). Cartão completo só para mídia, citações e avisos.
4. **Nada de border-radius 12–16px em tudo.** Raio 0–4px, consistente.
5. **Nada de Inter + Tailwind default.** Paleta e tipos próprios (abaixo). Sem framework CSS — CSS artesanal, custom properties, `@layer`, < 20 KB.
6. **Nada de microcopy inflada** ("Supercharge your feeds! 🚀"). Voz: declarativa, específica, pt-BR sóbrio. Mensagens de estado dizem o fato: *"Última leitura há 3 h, HTTP 304."*
7. **Assimetria funcional**: layout de 3 colunas com larguras desiguais e alinhamento a uma baseline grid — não o "tudo centrado max-w-2xl".
8. **Densidade real.** Produto de gente que lê 500 fontes; espaçamento generoso na coluna de leitura, denso nas listas.

### Tokens concretos (validar contraste WCAG 2.2 AA na tela, Fase 2)

**Tipografia — 3 papéis (proposta §8.5):**

| Papel | Fonte | Racional |
|---|---|---|
| Leitura (corpo de artigos) | **Source Serif 4** (variável, OFL), subsetada, self-hosted | Serifada desenhada para tela; séria sem ser Playfair-decorativa |
| Interface | **Stack de sistema** (`system-ui, -apple-system, "Segoe UI", Roboto…`) | 0 KB, aparência nativa de ferramenta real — Feedbin/GitHub fazem igual. Foge do "Inter em tudo" |
| Estado técnico | **JetBrains Mono** (OFL), subset mínimo (números, latino básico) | A voz "isto é fato do sistema" |

Medida de leitura 65–75ch, corpo ≥ 16px (18px na coluna de leitura), entrelinha 1.6, escala 1.2 (minor third) — não 1.25/1.333 default de gerador.

**Paleta — papel e tinta, um acento:**

| Token | Claro | Escuro (intencional, não inversão) |
|---|---|---|
| `--paper` (fundo) | `#FAF8F5` (branco quente) | `#16140F` (quase-preto quente) |
| `--ink` (texto) | `#211D16` | `#E8E4DC` |
| `--ink-2` (secundário) | `#6B6459` | `#9B948A` |
| `--rule` (divisores) | `#E4E0D8` | `#2A2720` |
| `--accent` (links/ações) | **azul-tinta `#31517B`** | `#7FA3CE` |
| `--ok` / `--warn` / `--err` (estado, nunca cor sozinha) | verde-oliva / âmbar / vermelho-tijolo dessaturados | idem, elevados |

Faixa de procedência de cada item: mono, `--ink-2`, 12–13px — `exemplo.org · via WebSub · há 4 min · v3`.

### Processo para não derrapar

- **Fase 0 entrega um "type specimen" estático** (`/dev/specimen`): todos os tokens, os 4 tipos de item (nota, artigo, resposta, podcast), claro/escuro, 3 densidades — revisado por você **no navegador antes** de qualquer tela real.
- Screenshots comparados com Feedbin/iA a cada fase de UI (a ferramenta de browser desta sessão faz isso).
- Nenhuma cor/fonte/raio fora dos tokens; lint de CSS acusa hex literal fora de `tokens.css`.

---

## Estrutura do repositório

Conforme proposta §4.5, com acréscimos:

```
rss-social/
  LICENSE                  AGPL-3.0
  README.md                inclui créditos (§2.3): Dave Winer, RSC/rmdes, IndieWeb, JSON Feed
  PROPOSTA.md              (existente — vira docs/PROPOSTA.md)
  compose.yaml             1 serviço (proposta §9.1)
  Dockerfile               multi-stage → FROM scratch, USER 65532, multi-arch
  cmd/rss-social/          main: serve|init|migrate|backup|restore|admin|doctor|version
  internal/
    feedin/ feedout/ convergence/ thread/ push/ ledger/ jobs/
    identity/ moderation/ safety/ store/ web/
  web/assets/              tokens.css, app.css, htmx.min.js, fontes subsetadas (embed)
  testdata/feeds/          corpus real versionado
  testdata/golden/         XML esperado byte a byte
  tools/threadwalker/      wrapper de CI p/ o walker do Dave (Node só no CI, nunca no build)
  docs/specs/  docs/decisions/   ADRs (0001 stack, 0002 HTMX>Datastar, 0003 driver SQLite…)
  .github/workflows/ci.yml
```

Regras: arquivo > 400 linhas é alerta, > 800 proibido. Dependência nova exige ADR.

---

## Fases de construção (tudo = versão 0.0.1)

Portas: app `11080`, admin `127.0.0.1:11090` (proposta §4.2). Orçamentos de recursos da §4.4.1 valem desde a Fase 0 e são verificados no CI.

### Fase 0 — Fundação e prova de interop
1. `git init`, licença AGPL-3.0, módulo Go 1.23+, CI (build, vet, test, `benchstat`, lint de tamanho de imagem)
2. Esqueleto do binário: config por env (`RSS_SOCIAL_*`), `slog`, `goose` embutido, `doctor`, `/healthz` `/readyz` `/metrics`
3. **Guard SSRF** em `internal/safety`: validação de IP no `Dialer.Control` (pós-DNS, por hop), bloqueio de loopback/link-local/privadas/multicast/v4-mapeado, 3 redirects revalidados, `io.LimitReader` 5 MB, timeouts — **com `go test -fuzz` incluindo DNS rebinding**
4. Parse (gofeed) contra corpus real em `testdata/feeds/` — incluir feeds do rss.chat e do RSC capturados
5. **Emissão RSS à mão** (`encoding/xml`): namespace `source:`, `guid` permalink nu, `source:comments` como feed por post, dual-emit RFC 4685, pass-through de fragmentos desconhecidos — **golden files byte a byte**
6. **Threadwalker no CI**: sobe instância efêmera com conversa semeada, o walker do Dave caminha e o build quebra se ele quebrar
7. Medição do driver SQLite (ncruces/wazero-pinned vs modernc): memória ociosa/pico, `mmap_size` → **ADR-0003 decide**
8. Type specimen em `/dev/specimen` (ver §Design) → **sua revisão visual no navegador**

**Portão:** binário que não faz nada útil e prova que a parte difícil está certa + specimen aprovado.

### Fase 1 — A espinha (ex-"v0.1")
- Conta local (argon2id, cookie assinado, link mágico via SMTP), papéis owner/admin/moderator/user, `admin create` só por CLI com link de uso único (§7.5)
- Ingestão 3 camadas: `raw_payload` (sha256, zstd, dedup) → `observation` (append-only) → `logical_item` com **convergência determinística** (§5.3: domínio do autor > updated > fidelidade > hash) e o "por quê" gravado
- Fila durável em SQLite (§5.5: lease, idem_key, dead-letter) + livro-razão `delivery_attempt` (§5.4)
- Assinar fonte por URL com descoberta; polling adaptativo + condicional (ETag/304) + pool de 4 workers com jitter; WebSub/rssCloud como assinante
- Timeline SSR (templ + HTMX), SSE de itens novos sem deslocar leitura; composição Markdown (goldmark→bluemonday); feeds RSS+JSON por usuário + firehose `/users/rss.xml`
- Threading resolve-once, órfãos honestos, adoção
- Moderação mínima: bloqueio, quarentena de fonte nova, rate limit, cadastro fechado/convite
- `backup`/`restore` com **restore automatizado em ambiente limpo no CI**

**Portão:** duas instâncias locais federam A→B→A por RSS puro no teste de integração; restore passa no CI.

### Fase 2 — Leitura séria (ex-"v0.2")
Não lidos · salvos · coleções · FTS5 · filtros · 3 densidades · apresentações por tipo (nota/artigo/podcast/resposta) · saúde da fonte no leitor (§8.4) · atalhos de teclado · **hoist/dehoist** (§8.3, com crédito ao rss.chat) · OPML import/export · sistema visual completo com auditoria WCAG 2.2 AA.
**Portão:** utilizável diariamente como leitor principal; zero falhas críticas de contraste.

### Fase 3 — Operação e moderação (ex-"v0.3")
Painel do operador completo (§9.4, cada erro com ação) · denúncias · silenciamento por palavra/domínio · log de auditoria (inclusive owner) · 2FA TOTP obrigatório p/ admin · reautenticação p/ ações destrutivas · métricas completas · migrações com rollback documentado · página pública de regras.
**Portão:** operador explica e recupera qualquer falha sozinho.

### Fase 4 — Domínio como identidade (ex-"v0.4")
IndieAuth (validar indielib antes) · `rel="me"` e `h-card` · reivindicação/desvinculação de domínio · estados de verificação explícitos na UI (conta local ≠ site verificado ≠ fonte).

### Fase 5 — Conversa entre sites (ex-"v0.5")
Webmention envio/recebimento com moderação prévia · Micropub · **tela Percurso** sobre o livro-razão · update/delete idempotentes · suíte de interop expandida.

### Fase 6 — Mídia (ex-"v0.6")
Upload de imagens/galerias · enclosures com player acessível · cotas · remoção de metadados sensíveis · backup incremental.

### Release 0.0.1
Tag `0.0.1`, imagem `ghcr.io` multi-arch < 40 MB, changelog, SECURITY.md, política de privacidade/LGPD (§7.4), página `/creditos`. Avisar o Ricardo (RSC) — custo zero, ganho provável (§13.6; confirmar com você antes de enviar qualquer mensagem).

---

## O que NÃO entra (vinculante, §11)

App nativo · E2EE/DMs · plugins · recomendação algorítmica · IA gerativa no produto · microserviços · multi-banco · CDN · monetização · ActivityPub completo.

---

## Verificação end-to-end

1. **CI em todo push:** build estático sem cgo · testes + fuzz (SSRF, parser, resolvedor) · golden files · threadwalker · `benchstat` (regressão de alocação quebra) · teto de imagem 40 MB · restore em ambiente limpo
2. **Teste de federação:** compose com 2 instâncias, script publica em A, responde em B, verifica adoção da thread em A — tudo por RSS
3. **Orçamento (§4.4.1):** carga com corpus de 500 feeds; RSS < 180 MB, CPU < 6%, maioria das leituras devolvendo 304; boot < 1 s; página < 50 KB HTML + 30 KB JS + 20 KB CSS
4. **Convergência:** teste property-based — mesmas observações em ordens embaralhadas ⇒ mesmo `logical_item`
5. **Visual:** specimen e telas revisados no navegador (browser da sessão) contra as regras anti-IA; screenshots claro/escuro/3 densidades; comparação lado a lado com Feedbin
6. **Segurança:** suíte XSS contra bluemonday+CSP · SSRF via redirect e rebinding · portão 11090 inacessível de fora no compose

## Riscos além dos da proposta (§14)

| Risco novo | Mitigação |
|---|---|
| `ncruces` mudou para wasm2go (recente, instável) | Pinar v0.33.2 (última com wazero); ADR-0003 após medição |
| Node no CI para o threadwalker | Contido em `tools/`, nunca no build nem na imagem |
| Design derrapar para genérico sob pressão de velocidade | Specimen aprovado antes de telas; lint de tokens; revisão visual por fase |
