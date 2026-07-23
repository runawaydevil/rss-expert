---
title: "RSS Social — leitor social da web aberta"
subtitle: "Proposta revisada: implementação limpa em Go"
date: 2026-07-23
status: rascunho para análise — nada implementado
substitui: "2026-07-22-pesquisa-026-ideia-afluente-plataforma-social-feeds.md"
---

# RSS Social

> Cada site mantém sua origem. A conversa corre em comum.

Documento de proposta. Não é plano de implementação, não é spec técnica final.
É a ideia refinada até o ponto em que dá para discordar dela com precisão.

---

## 0. O que mudou em relação ao documento original

O documento anterior (*Afluente*, 22/07) foi escrito a partir da leitura do
README, da inspeção visual do app público e das especificações. Esta revisão
foi escrita depois de **clonar e ler o código** do [RSC](https://github.com/rmdes/rsc)
e do [rss.chat](https://github.com/scripting/rss.chat), incluindo os 120
documentos de spec/plano/review do primeiro.

Sete mudanças de rumo:

| # | Antes | Agora | Por quê |
|---|---|---|---|
| 1 | Fork responsável do RSC | **Implementação limpa, conhecimento emprestado** | O RSC está reescrevendo o modelo de dados inteiro agora; e o "núcleo caro" é menor do que parecia |
| 2 | TypeScript · Hono · SvelteKit · Node | **Go · templ · HTMX · SQLite** | Decisão do projeto; e habilita o binário único |
| 3 | Docker Compose de 4–5 serviços | **Um binário estático, um container `FROM scratch`** | Diferencial de produto real para auto-hospedagem |
| 4 | Item bruto → publicação normalizada (2 camadas) | **Payload → observação → item lógico (3 camadas)** | O mesmo post chega por 3 caminhos diferentes; 2 camadas não resolvem |
| 5 | "Percurso" como tela | **Livro-razão de entrega append-only** | Uma estrutura serve Percurso + admin + debug + auditoria |
| 6 | Moderação distribuída pelo roteiro | **Moderação e limites no v0.1** | Público real = abuso no dia 1 |
| 7 | Vocabulário metafórico completo | **Nomes planos, exceto "Percurso"** | A metáfora era do nome *Afluente*; com *RSS Social* ela perde a âncora |

O diagnóstico de **produto** do documento original continua de pé e é a espinha
desta proposta. O que mudou foi a premissa técnica e o rigor do modelo de dados.

---

## 1. Diagnóstico: o que a pesquisa no código revelou

### 1.1 O RSC está em cirurgia aberta

Último commit em **23/07/2026, 15:17** — o mesmo dia desta proposta. O projeto
está no meio de uma reescrita em quatro verticais, atrás da flag
`RSC_SOURCE_MODEL_V2=off`, com estratégia declarada de *não fazer dual-write* e
**cutover atômico no final**:

| Vertical | Substitui | Status em 23/07 |
|---|---|---|
| V1 — Source Control Plane | Registro de fontes, resolução de URL, governança, auditoria | Implementação iniciada (3 commits) |
| V2 — Logical Items & Ordinary Reads | Itens lógicos, convergência, threading, visibilidade, feeds, SSE | Plano aprovado |
| V3 — Moderation/Events/Verification | Moderação, tombstones, verificação de origem, evidência | Plano aprovado |
| V4 — Migration/Cutover | Conversão atômica dos dados legados | Plano aprovado |

São ~3.000 linhas de spec e ~2.900 de plano. **Forkar hoje é congelar o paciente
na mesa.** E, ironicamente, o upstream está construindo exatamente o que o
documento original listava como diferencial do Afluente: moderação, verificação
de origem e convergência determinística.

### 1.2 O "núcleo técnico caro" é pequeno

```
core (backend)                     ~3.900 LOC   (sqlite.ts sozinho: 955)
web  (SvelteKit)                   ~4.000 LOC   + 939 linhas de CSS
total TS/Svelte                   ~14.400 LOC
docs (120 arquivos .md)           ~25.000 linhas  ← maior que o código
```

Dentro do core, o que é genuinamente difícil de refazer:

- `ingest.ts` (268 linhas) — threading resolve-once, orfandade honesta, adoção fora de ordem
- `feed.ts` (236) — emissão RSS com namespace `source:`, dual-emit com RFC 4685
- `push.ts` + `push-in.ts` (525) — WebSub e rssCloud, ida e volta
- `push-guard.ts` (**58**) — o guard SSRF inteiro
- `opml.ts` (146) + `discovery.ts` (91)

**~1.500 linhas de lógica de domínio realmente não-trivial.**

E o que o documento original chamou de "mais fácil de subestimar" — o parser de
RSS/Atom/JSON Feed/OPML — **não está no RSC**. É a biblioteca `feedsmith`.
Microformats, identidade, markdown e sanitização também são bibliotecas.
Go tem equivalente maduro para cada uma (§4).

### 1.3 O ativo real é conhecimento de interoperabilidade

Isto é o que faz uma conversa federar A→B→A por RSS puro. Extraído do código,
independe de forkar:

```xml
<rss version="2.0" xmlns:source="http://source.scripting.com/">
  <channel>
    <source:account>…</source:account>          <!-- channel-level, NÃO item-level -->
    <item>
      <guid isPermaLink="true">https://exemplo.org/p/42</guid>
      <source:markdown>texto **em markdown**</source:markdown>
      <source:inReplyTo isPermaLink="false">…</source:inReplyTo>
      <thr:in-reply-to ref="…"/>                <!-- RFC 4685, fallback -->
      <source:comments count="7" feedUrl="https://exemplo.org/p/42/replies.xml"/>
    </item>
  </channel>
</rss>
```

As regras que importam:

1. **`guid` = permalink nu** é a chave de thread. É isso que faz o
   [`threadwalker`](https://github.com/scripting/rss.chat/tree/main/examples/threadwalker)
   do Dave Winer reconstruir uma conversa **sem alteração nenhuma**.
2. **`source:comments` é um feed RSS por post.** A thread inteira é caminhável
   um feed por vez, todo nível com a mesma forma. Recursão sobre XML estático,
   sem API.
3. **`source:markdown` é o marcador de detecção.** É assim que se identifica um
   par Textcasting: um feed cujos itens carregam esse elemento.
4. **`source:account` é channel-level.** O RSC chegou a emitir por item para
   agradar uma versão antiga do threadwalker, e depois voltou ao spec.
5. **Edição viaja no `guid` estável** com marcador `<atom:updated>` — sem
   ressuscitar o post no topo da timeline.

### 1.4 O rss.chat é outro animal

Também com commit hoje. **MySQL**, não SQLite. **WebSocket**, não SSE. Firehose
com framing `verbo\r{json}` e dois verbos (`newItem`, `updatedItem`), campos
copiados do FeedLand de propósito — *"gratuitously renaming things is what makes
interop hard"*. Auth sem senha: par `emailaddress` + `emailcode`. API por
query-string. Cada escrita republica os feeds estáticos do autor, do pai e do
feed de comentários do pai.

E uma ideia de interface que o documento original não tinha: **hoist / dehoist** —
zoom em um nível da conversa, herdado de outliners. Ver §8.3.

### 1.5 Textcasting é um contrato comportamental

Não é schema, é manifesto — *"interop entre apps sociais baseado nas features de
que escritores precisam"*. As exigências:

- títulos **opcionais**
- estilo (negrito, itálico) que **sobrevive ao trânsito**
- links de qualquer palavra para qualquer URL
- **sem limite de tamanho**
- **edição que se propaga** depois de publicado
- Markdown ao lado do HTML
- enclosures (áudio, vídeo, imagem)
- **pass-through**: um app que não entende um elemento deve **repassá-lo**, não descartá-lo

O último é o mais ignorado do mercado — e o mais barato de honrar (§6.5).

---

## 2. Decisão estratégica: implementação limpa em Go

### 2.1 As três opções, honestamente

| | Fork do RSC | Contribuir upstream | **Implementação limpa em Go** |
|---|---|---|---|
| Aproveita código | ~1.500 linhas úteis, em TS | Todo | Nenhum |
| Aproveita conhecimento | Todo | Todo | **Todo** (§1.3) |
| Risco de timing | **Alto** — reescrita em curso | Baixo | Nenhum |
| Liberdade de stack | Nenhuma (preso a Node) | Nenhuma | **Total** |
| Valor de aprendizado | Médio (ler código alheio) | Baixo | **Alto** |
| Dívida com terceiros | Merge de upstream para sempre | Roadmap alheio | Nenhuma |
| Tempo até algo rodando | Rápido | Rápido | Mais lento |

### 2.2 A escolha e o porquê

**Implementação limpa em Go.** Três razões que se somam:

1. **A stack é decisão sua, e ela é incompatível com fork.** Forkar um projeto
   TypeScript para reescrevê-lo em Go não é fork — é reescrita com passivo
   jurídico e histórico Git de outra pessoa. Não faz sentido.

2. **O que faria o fork valer a pena não existe lá.** As três camadas que o
   documento original define como prioridade real — confiabilidade da base,
   leitura excelente, operação e moderação — reaproveitam quase nada do código
   do RSC. São justamente o que não está pronto lá.

3. **É um laboratório de aprendizado.** Ler código alheio ensina; construir o
   modelo de dados difícil ensina muito mais. E os problemas difíceis aqui —
   convergência determinística, entrega idempotente, SSRF, filas duráveis — são
   exatamente os que valem aprender.

### 2.3 Postura ética e jurídica

Implementação limpa significa: **não copiamos código.** A licença MIT do RSC não
se aplica a código que não usamos. Mas a decência se aplica de qualquer jeito:

- **Creditar visivelmente**, no README e numa página `/creditos`:
  - Dave Winer — RSS 2.0, OPML, rssCloud, o manifesto Textcasting, o namespace `source:` e o rss.chat
  - Ricardo (rmdes) e o RSC — a prova de que a coisa funciona, e o mapa de interop que estudamos
  - Comunidade IndieWeb — Micropub, Webmention, IndieAuth, microformats2
  - JSON Feed — Manton Reece e Brent Simmons
- **Não sugerir endosso** de nenhum deles.
- **Devolver o que descobrirmos**: bugs de interop, casos de feed do mundo real,
  divergências de spec. Isso vai para os projetos de origem, não fica aqui.
- Licença própria: **MIT** ou **AGPL-3.0** — decisão em aberto (§13).

---

## 3. O produto

### 3.1 Categoria

> **Leitor social da web aberta.**

Não é "um Mastodon de RSS". Não é um agregador. É um leitor de feeds excelente
que, por baixo, também publica e conversa — e que trata a origem de cada
conteúdo como informação de primeira classe.

### 3.2 Público

Instância **pública** — qualquer pessoa pode se cadastrar (sujeito à política de
admissão configurada). Isso muda três coisas em relação a uma instância pessoal:

- **Abuso é premissa, não exceção.** Moderação e limites entram no v0.1.
- **Perfis públicos são superfície de ataque e de assédio.** Bloqueio, silenciamento e denúncia desde o começo.
- **LGPD é real.** Instância brasileira, dados de terceiros, canal de remoção, política de privacidade. Não é opcional.

Perfil de quem deve gostar: quem já usa RSS e quer conversa; autores com blog
próprio; comunidades IndieWeb e small web; operadores de instâncias pequenas;
curadores de diretórios.

**Não é para:** milhões de usuários, celebridades, feed algorítmico, descoberta viral.

### 3.3 Proposta de valor, por papel

| Papel | Promessa |
|---|---|
| Quem lê | Tudo que você acompanha, sem algoritmo e sem perder o contexto — e você sempre sabe de onde veio |
| Quem publica | Seu domínio é sua identidade; nós distribuímos e reunimos as conversas |
| Quem hospeda | Um binário. Um arquivo de banco. Um comando de backup. E um painel que mostra o que quebrou |

### 3.4 O nome

**RSS Social** é honesto e posiciona instantaneamente. Custos que aceitamos
conscientemente:

- Genérico demais para registrar como marca
- Difícil de buscar (colide com o termo comum)
- Amarra o produto ao nome de um formato

Para um laboratório público, o benefício (ninguém precisa te perguntar o que é)
supera. **Consequência de vocabulário:** os nomes metafóricos do documento
original (*Corrente, Nascentes, Margem, Confluência*) derivavam da metáfora do
rio em *Afluente*. Sem essa âncora, viram arbitrários. Recomendação:

| Conceito | Nome na interface |
|---|---|
| Timeline principal | **Feed** |
| Site ou feed acompanhado | **Fonte** |
| Grupo de fontes | **Coleção** |
| Thread de respostas | **Conversa** |
| Item salvo | **Salvos** |
| Estado de entrega de uma resposta | **Percurso** ← único termo não-óbvio, e merece nome próprio |
| Diretório de descoberta | **Descobrir** |

---

## 4. Arquitetura

### 4.1 A tese do binário único, entregue como um container

**Docker é o alvo de execução oficial.** A vantagem do Go aqui não é evitar
container — é fazer o container ser trivial:

```
RSC hoje:        Compose + Caddy + Node(core) + Node(web) + Mailpit   → 4–5 serviços
RSS Social:      Compose + rss-social                                 → 1 serviço
```

Go compila estático. Com um driver SQLite **sem cgo** e `embed` para templates,
CSS, JS e migrações, o binário não depende de nada — o que habilita a imagem
mínima possível:

```dockerfile
FROM scratch
COPY rss-social /rss-social
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
VOLUME /data
EXPOSE 11080
ENTRYPOINT ["/rss-social", "serve"]
```

Três consequências que valem mais que a economia de disco:

1. **~30 MB de imagem** contra centenas de MB de uma base Node ou Alpine
2. **Zero superfície de CVE de distribuição.** Não há `apt`, `apk`, glibc,
   shell ou gerenciador de pacotes dentro da imagem. Não há o que patchear —
   só o nosso binário
3. **Um processo, um container.** Sem supervisor, sem `entrypoint.sh` de 80
   linhas, sem serviço auxiliar obrigatório

Um processo separado de proxy continua **opcional**: TLS pode vir de `autocert`
embutido, de um Caddy/Traefik na frente, ou do proxy que o operador já tem.

Para o público-alvo — pessoas hospedando instâncias pequenas — essa é a
diferença entre 5 e 50 instalações. E é o argumento mais forte que temos contra
"por que não uso o RSC?".

### 4.2 Portas

Bloco reservado do projeto: **`11000–11099`**. Nada abaixo de 11000.

| Porta | Serviço | Exposição |
|---|---|---|
| **`11080`** | HTTP da aplicação | Publicada; atrás de proxy reverso em produção |
| **`11090`** | Painel administrativo + `/metrics` + `/healthz` | **Loopback por padrão** (`127.0.0.1:11090`), §7.5 |
| `11081` | TLS direto, quando `autocert` embutido está ativo | Opcional, só sem proxy externo |

Todas configuráveis por variável de ambiente (`RSS_SOCIAL_LISTEN`,
`RSS_SOCIAL_ADMIN_LISTEN`). Os valores acima são os **padrões**, escolhidos para
não colidir com nada comum (8080, 3000, 5432, 6379, 9090, 9100) numa máquina que
já hospeda outras coisas.

### 4.3 Stack

| Camada | Escolha | Por quê |
|---|---|---|
| Linguagem | **Go 1.23+** | Binário estático, concorrência nativa para ingestão, stdlib forte em HTTP/XML/crypto |
| HTTP | **`net/http` da stdlib** (roteamento com padrões de método, Go 1.22+) | Sem framework. Menos superfície, menos churn |
| Templates | **[`templ`](https://templ.guide)** | Compilado, type-safe, sem overhead de runtime. Erro de UI vira erro de compilação |
| Interatividade | **HTMX** + ilhas mínimas de JS | SSR é o modo nativo; "funciona sem JS" sai de graça, não como esforço |
| Tempo real | **SSE** (`http.Flusher` da stdlib) | Unidirecional, HTTP puro, reconecta sozinho. ~40 linhas |
| Banco | **SQLite**, modo WAL | Um arquivo. Backup é copiar. Sem serviço extra |
| Driver SQLite | **[`ncruces/go-sqlite3`](https://github.com/ncruces/go-sqlite3)** | Sem cgo (SQLite compilado para WASM, traduzido para Go puro). FTS5, JSON, vtables. ~3× mais rápido que `modernc` em leitura nos benchmarks públicos. **Custo:** mais memória (sandbox WASM) — validar cedo |
| Migrações | **[`goose`](https://github.com/pressly/goose)** com SQL embutido via `embed` | Versionadas, aplicadas no boot, com dry-run |
| Parse de feeds | **[`gofeed`](https://github.com/mmcdole/gofeed)** (RSS/Atom/JSON Feed 1.0+1.1) | Guarda namespaces desconhecidos em `Item.Extensions` — exatamente o que `source:` precisa |
| Emissão de feeds | **`encoding/xml` à mão** | Controle total do namespace `source:`, da ordem dos elementos e do pass-through. Bibliotecas de geração não dão isso |
| Markdown | **[`goldmark`](https://github.com/yuin/goldmark)** | GFM, extensível, é o motor do Hugo |
| Sanitização HTML | **[`bluemonday`](https://github.com/microcosm-cc/bluemonday)** | Padrão de fato em Go. É o portão de XSS |
| Microformats2 | **[`willnorris.com/go/microformats`](https://pkg.go.dev/willnorris.com/go/microformats)** | h-feed / h-entry / h-card, v1 e v2 |
| IndieAuth + Micropub | **[`go.hacdias.com/indielib`](https://go.hacdias.com/indielib)** | Toolkit IndieWeb em Go — **a verificar antes de adotar** |
| Webmention | **[`willnorris.com/go/webmention`](https://willnorris.com/go/webmention)** | Do mesmo autor do parser mf2 |
| Fila | **Tabela em SQLite, implementação própria** (§5.5) | ~250 linhas. Sem Redis. Sem serviço extra |
| Busca | **FTS5 do SQLite** | Já está no driver |
| Sessões / senhas | **Próprio**, cookie assinado + `argon2id` (`golang.org/x/crypto`) | Go não tem um `better-auth`; a superfície é pequena o bastante para ser nossa |
| E-mail | **SMTP** via `net/smtp` ou `go-mail` | Produção exige SMTP real, sem exceção |
| Logs | **`log/slog`** estruturado, stdlib | JSON em produção, texto no dev |
| Métricas | **`prometheus/client_golang`** + `/healthz` `/readyz` | Lab de aprendizado: é justamente o que vale aprender |

**O que NÃO entra:** Redis, PostgreSQL, MongoDB, ORM, microserviços, Node no
build, CDN própria. Cada um entra quando **medição real** demonstrar necessidade
— não por antecipação.

### 4.4 Orçamento de recursos — leveza como requisito, não como intenção

A instância precisa ser **boa vizinha** na máquina: rodar ao lado de outras
coisas sem competir por CPU, memória ou disco. "Leve" só significa alguma coisa
se tiver número — abaixo estão os tetos, e eles são verificados no CI (§4.4.6).

#### 4.4.1 O orçamento

| Cenário | Memória (RSS) | CPU (média) | Observação |
|---|---|---|---|
| Ocioso, 0 assinaturas | **< 40 MB** | ~0% | Sem assinatura não há trabalho de fundo. Nada de timer girando à toa |
| 100 fontes, regime | **< 90 MB** | **< 2%** de um núcleo | Instância pessoal típica |
| 500 fontes, regime | **< 180 MB** | **< 6%** de um núcleo | Instância comunitária |
| Pico de ingestão | **< 300 MB** | 1 núcleo por poucos segundos | Limite do container: **512 MB** |

| Artefato | Teto |
|---|---|
| Imagem Docker | **< 40 MB** |
| Boot frio até responder | **< 1 s** |
| HTML da timeline | **< 50 KB** |
| JavaScript total da página | **< 30 KB** (HTMX são ~14 KB comprimidos) |
| CSS total | **< 20 KB** |

Zero build de Node. Zero framework de frontend. Fontes auto-hospedadas e
**subsetadas**, só os pesos realmente usados.

#### 4.4.2 Onde um leitor de feeds engorda — e o que fazemos

**1. Tempestade de polling.** 500 fontes a cada 5 minutos são 6.000
requisições/hora, sempre. Três defesas somadas:

- **Push primeiro.** Fonte com WebSub ou rssCloud **não é pesquisada** — só
  recebe. Polling vira rede de segurança diária, não o mecanismo principal
- **Polling adaptativo.** Medimos a cadência real de cada fonte e o intervalo
  vira `clamp(mediana_entre_posts / 2, 15min, 24h)`. Blog que publica uma vez
  por mês é lido uma vez por dia — não a cada 15 minutos
- **Requisição condicional.** `ETag` e `If-Modified-Since` em tudo. Um `304` é
  ~200 bytes e zero parsing. Na prática, a grande maioria das leituras é 304

**2. Concorrência sem limite.** Uma goroutine por fonte parece Go idiomático e é
uma armadilha: 500 fetches e 500 parsers simultâneos estouram memória. **Pool de
workers limitado** (padrão 4, configurável), fila durável fazendo o
enfileiramento, e **jitter** no agendamento para as fontes não dispararem juntas.

**3. Payloads grandes.** `io.LimitReader` com teto por resposta (padrão 5 MB) e
timeout total — já exigido pelo guard SSRF (§7.1), aqui pela mesma razão
econômica.

**4. O custo do modelo de 3 camadas (§5.2).** Guardar bytes brutos é o que dá
reprocessamento e auditoria — e cresce sem parar se ninguém cuidar. Mitigação:

- **Endereçamento por conteúdo**: payload idêntico é gravado uma vez só
- **Compressão zstd** no payload bruto (feeds XML comprimem 8–10×)
- **Retenção assimétrica**: item que convergiu sem conflito mantém o bruto por
  **30 dias**; item com **conflito, disputa ou moderação mantém para sempre**.
  Guardamos evidência onde evidência importa, não em tudo
- `VACUUM` incremental agendado, não `VACUUM` completo bloqueando o banco

**5. Cache de imagens remotas.** Desligado por padrão. Quando ligado: teto de
disco, TTL, e despejo LRU. Cache sem teto é vazamento de disco com outro nome.

#### 4.4.3 SQLite bem configurado

Os padrões do SQLite são conservadores demais para servidor. Ajustes na abertura:

```sql
PRAGMA journal_mode = WAL;        -- leitores não bloqueiam o escritor
PRAGMA synchronous  = NORMAL;     -- seguro com WAL; FULL custa caro sem ganho aqui
PRAGMA busy_timeout = 5000;       -- espera em vez de devolver SQLITE_BUSY
PRAGMA cache_size   = -20000;     -- 20 MB de cache de páginas
PRAGMA temp_store   = MEMORY;
PRAGMA foreign_keys = ON;
```

E o padrão de conexões que evita `SQLITE_BUSY` em Go: **um único pool escritor
com `SetMaxOpenConns(1)`** e um pool separado de leitores. SQLite tem um
escritor só — modelar isso explicitamente é mais rápido e mais previsível do que
descobrir na produção.

> `mmap_size` fica **em aberto**: o driver WASM (`ncruces/go-sqlite3`) pode não
> se beneficiar como o driver com cgo. Medir na Etapa 0, junto da memória (§13.4).

#### 4.4.4 Go dentro de container

- **`GOMEMLIMIT` obrigatório**, em ~80% do limite de memória do container. Sem
  ele, o coletor do Go não sabe do limite do cgroup e o kernel mata o processo
  por OOM antes do GC reagir. Com ele, o GC fica mais agressivo perto do teto e
  o processo sobrevive
- `GOMAXPROCS` respeitando a cota de CPU do container, não o número de núcleos
  da máquina hospedeira
- Sem `cgo` — nada de threads de runtime C fora do controle do escalonador do Go

#### 4.4.5 Consultas

Sem N+1 na timeline: uma consulta paginada por cursor com `JOIN`, nunca uma
consulta por item. `EXPLAIN QUERY PLAN` em toda consulta do caminho quente,
revisado quando o schema muda. Índice de cobertura para a timeline e para
"não lidos". SSE com **uma goroutine por conexão, com teto e timeout de
ociosidade** — não uma por aba aberta para sempre.

#### 4.4.6 O orçamento é testado, não prometido

- `go test -bench` com **`benchstat` no CI**: regressão de alocação ou de tempo
  no caminho quente **quebra o build**
- Teste de carga com o corpus de 500 feeds medindo memória de pico e CPU
  acumulada, comparado contra a tabela de §4.4.1
- `/metrics` expõe memória, profundidade de fila e taxa de 304 — o operador
  enxerga a saúde de recursos sem instalar nada

**Regra que fecha:** dependência nova que aumente o binário, a imagem ou o
orçamento de tempo de execução precisa de justificativa registrada em
`docs/decisions/`. Leveza se perde um pacote por vez.

### 4.5 Forma do repositório

```
cmd/rss-social/          binário único (serve, migrate, backup, doctor)
internal/
  feedin/                fetch, parse, descoberta, normalização
  feedout/               emissão RSS/JSON/OPML, namespace source:
  convergence/           payload → observação → item lógico (§5.2)
  thread/                threading resolve-once, órfãos, adoção
  push/                  WebSub + rssCloud, entrada e saída
  ledger/                livro-razão de entrega (§5.4)
  jobs/                  fila durável em SQLite (§5.5)
  identity/              contas, sessões, IndieAuth, verificação de domínio
  moderation/            bloqueios, quarentena, denúncias, purge
  safety/                guard SSRF, sanitização, limites (§7)
  store/                 SQLite: schema, queries, migrações
  web/                   handlers, templ, HTMX, SSE
testdata/
  feeds/                 corpus de feeds reais, versionado
  golden/                saída XML esperada, byte a byte
docs/
  specs/                 uma spec por peça
  decisions/             ADRs
```

Regra: **arquivo com mais de 400 linhas é sinal de que faz coisa demais.**
Nenhum acima de 800.

---

## 5. O modelo de dados — onde está o valor real

Esta seção é a maior adição desta revisão. O documento original propunha duas
camadas (item bruto → publicação normalizada). Isso não resolve o problema
central.

### 5.1 O problema que ninguém vê antes de sangrar

O mesmo post chega por **três caminhos diferentes**, em momentos diferentes, com
conteúdos diferentes:

```
        ┌── feed pessoal do autor          → tem source:markdown, versão 3
post ───┼── firehose /users/rss.xml        → só HTML, versão 2
        └── push WebSub da instância dele  → tem markdown, versão 3, chegou primeiro
```

Se você guarda "o item", a última escrita ganha e o resultado é aleatório.
Um post editado pode voltar à versão antiga porque um poll lento chegou depois
de um push rápido. É corrupção silenciosa — o pior tipo.

### 5.2 Três camadas, não duas

```
┌─────────────────────────────────────────────────────────────┐
│  raw_payload    bytes exatos + sha256 + origem + quando      │
│                 endereçado por conteúdo → dedup de graça     │
│                 permite reprocessar com parser corrigido     │
└──────────────────────────┬──────────────────────────────────┘
                           │ parse
┌──────────────────────────▼──────────────────────────────────┐
│  observation    uma leitura, de uma fonte, num instante      │
│                 (fonte, payload, campos parseados, updated)  │
│                 nunca sobrescrita — só acumula               │
└──────────────────────────┬──────────────────────────────────┘
                           │ convergência determinística
┌──────────────────────────▼──────────────────────────────────┐
│  logical_item   a identidade, com a versão vencedora         │
│                 chave: guid canônico (ou link, ou hash)      │
│                 guarda POR QUE essa versão venceu            │
└─────────────────────────────────────────────────────────────┘
```

**Consequências que valem o custo:**

- Corrigir o parser não perde nada — reprocessa `raw_payload`
- Deduplicação sai de graça pelo hash
- A UI pode mostrar "esta versão veio de X, às Y, por Z"
- Auditoria de "o que exatamente aquele site publicou" é possível

### 5.3 Convergência determinística e explicável

A regra de seleção, em ordem, **documentada e testada**:

1. Observação de **origem reivindicada pelo domínio do autor** vence origem terceira
2. `atom:updated` (ou `pubDate` na falta) mais recente vence
3. Empate → **maior fidelidade** vence (tem `source:markdown` > só HTML)
4. Empate → **hash lexicograficamente menor** vence

O passo 4 é o que importa: é **determinístico, não cronológico**. Duas
instâncias que receberam as mesmas observações em ordens diferentes chegam ao
**mesmo resultado**. Sem isso, federação diverge.

E a interface expõe isso: um item mostra, sob demanda, qual observação venceu e
por qual regra. *Aparência robusta é um sistema que explica suas próprias
decisões.*

### 5.4 Livro-razão de entrega — uma estrutura, quatro produtos

O "Percurso" do documento original vira uma **tabela append-only**:

```sql
delivery_attempt(
  id, logical_item_id, target, protocol,   -- websub | rsscloud | webmention
  attempt_no, http_status, latency_ms,
  outcome,                                 -- pending|ok|rejected|failed|gaveup
  error_kind, error_detail, at
)
```

A mesma tabela serve:

1. **A tela Percurso** do autor — "sua resposta chegou?"
2. **O painel do operador** — o que está falhando, e por quê
3. **Debug** — reproduzir uma entrega específica
4. **Auditoria** — registro imutável do que foi tentado

Nada é atualizado, só inserido. O estado atual é a última linha.

### 5.5 Fila durável em SQLite

```sql
job(
  id, kind, payload_json,
  run_after,             -- backoff exponencial com jitter
  lease_until,           -- visibility timeout: worker morto libera sozinho
  attempts, max_attempts,
  idem_key UNIQUE,       -- reexecutar não duplica
  last_error, created_at
)
```

Workers pegam trabalho com `UPDATE … RETURNING` sob transação. Sem Redis, sem
serviço extra, sem perder trabalho quando o processo cai. Dead-letter depois de
`max_attempts`, visível no admin com botão de reexecutar.

**Idempotência é requisito, não desejo:** reprocessar qualquer job duas vezes
não pode duplicar post, resposta ou entrega.

### 5.6 Entidades

conta · **papel** (owner/admin/moderator/user, §7.5) · identidade · site
verificado · fonte · assinatura · coleção · `raw_payload` · `observation` ·
`logical_item` · revisão · conversa · relação de resposta · webmention ·
`delivery_attempt` · arquivo · regra de moderação · denúncia · `job` ·
evento de auditoria

---

## 6. Interoperabilidade — o contrato de fio

### 6.1 Entrada

RSS 2.0 · Atom · JSON Feed 1.0 e 1.1 · `h-feed`/microformats2 · OPML (com
categorias preservadas) · descoberta de feed a partir de página HTML
(`<link rel=alternate>`) · `ETag`/`Last-Modified` condicional · WebSub e
rssCloud como assinante.

### 6.2 Saída

Feed RSS e JSON por usuário · firehose da instância em `/users/rss.xml`
(convenção do rss.chat) · feed de comentários por conversa · OPML da lista de
assinaturas · WebSub fat-ping e rssCloud como publicador.

### 6.3 O namespace `source:`

Emitir e consumir, conforme §1.3. Regra dura: **`guid` é o permalink nu.**
É o que mantém a compatibilidade com o threadwalker.

### 6.4 Threading

`source:inReplyTo` preferido, `thr:in-reply-to` (RFC 4685) como fallback.
Resolve-once. **Orfandade honesta**: uma resposta cujo pai não foi resolvido
aparece marcada como órfã, não escondida nem inventada. **Adoção**: quando o pai
chega depois, o órfão se encaixa.

### 6.5 Pass-through — o requisito que ninguém honra

O Textcasting exige que um app repasse elementos que não entende. Ninguém faz.
É barato:

- Guardar os fragmentos XML desconhecidos junto da observação
- Re-emitir na saída, com o namespace preservado

Custo: uma coluna e ~60 linhas. Ganho: somos o único leitor da rede que não
destrói informação alheia ao trafegá-la. É um argumento de marca, não só técnico.

### 6.6 Suíte de conformidade como portão de CI

Não é uma promessa vaga de "testar interop". É concreto:

1. **Corpus versionado** em `testdata/feeds/` — feeds reais, incluindo os
   quebrados, com seus bugs preservados
2. **Golden files** — a saída XML esperada, byte a byte. Emissão de feed é
   contrato; se o byte mudou, alguém precisa aprovar
3. **`go test -fuzz`** no parser, no resolvedor de URL e no guard SSRF —
   fuzzing é nativo em Go e este é o caso de uso perfeito
4. **O threadwalker do Dave roda contra nossos próprios feeds no CI.**
   Se o walker dele quebrar, o build quebra. Não há teste de interop mais
   honesto que a ferramenta de referência da outra parte

---

## 7. Segurança

### 7.1 SSRF — do jeito certo em Go

Validar a URL **não basta**. `http://evil.com` pode resolver para `127.0.0.1`
depois da checagem (DNS rebinding). A técnica correta em Go:

```go
// Valida o IP DEPOIS da resolução DNS e ANTES de conectar — em cada hop.
dialer := &net.Dialer{
    Control: func(network, address string, c syscall.RawConn) error {
        return denyPrivateAddress(address) // address já é IP:porta resolvido
    },
}
```

Bloquear: loopback, link-local (`169.254.0.0/16` — inclui metadados de nuvem),
redes privadas, multicast, `::1`, IPv4-mapeado em IPv6, `0.0.0.0`.

Somado a: só `http`/`https` · limite de redirecionamentos (3) e **revalidação a
cada hop** · timeout de conexão e total · limite de bytes (`io.LimitReader`) ·
validação de `Content-Type` · **nunca** enviar cookies ou credenciais · pool de
saída separado do servidor web · registrar destino final e motivo do bloqueio.

Isso vale para **todo** fetch remoto: feed, avatar, imagem, verificação de
domínio, envio de webmention.

### 7.2 Conteúdo e navegador

Sanitização de HTML **no servidor**, sempre, com `bluemonday` — nunca confiar no
cliente. CSP restrita com nonce por requisição. Zero script vindo de feeds.
`iframe` desabilitado por padrão. Imagens remotas com política configurável
(proxy, ou bloquear, ou permitir — decisão do operador, não default silencioso).
A mesma cadeia de markdown na prévia e na publicação — o que você vê é o que sai.
CSRF em toda ação autenticada. Rate limit em login, publicação, assinatura e
callbacks de federação.

### 7.3 Instância pública

Cadastro aberto / fechado / por convite / com aprovação — configurável.
Quarentena para fonte remota nova. Aprovação de Webmention antes de exibir.
Limites por conta, por domínio e por faixa de rede. Bloqueio e silenciamento de
pessoa, domínio, fonte, palavra e conversa. Denúncia com contexto. Registro de
decisão administrativa. Página pública de regras e contato.

### 7.4 Direitos autorais e LGPD

Feed público não é domínio público. Preservar autor, fonte, URL original e data.
Exibir apenas o que o feed forneceu. Respeitar `410 Gone`, remoções e edições.
Permitir que o operador reduza a exibição a resumo + link. Canal documentado de
solicitação de remoção. Política de privacidade e de retenção. Exportação e
exclusão de conta a pedido.

### 7.5 Administração e papéis

Numa instância pública, o modelo administrativo é a primeira coisa que um
atacante procura. Ele é especificado aqui, não improvisado depois.

#### Como nasce o primeiro administrador

Pelo binário, no servidor — **nunca** pela web:

```bash
./rss-social admin create --email=voce@exemplo.org
```

Imprime no terminal um **link de setup de uso único, com validade curta**. O
operador abre, define senha e 2FA, e o link morre.

Três coisas explicitamente proibidas:

- **Senha padrão.** Nenhuma. Nem `admin/admin`, nem gerada e gravada em arquivo.
- **"O primeiro que se cadastrar vira dono."** Numa instância pública isso é uma
  corrida que o operador pode perder para um estranho.
- **Qualquer caminho de virar administrador pela interface web.**

#### Quatro papéis, não dois

| Papel | Pode | Não pode |
|---|---|---|
| **owner** (um só) | Tudo: promover e rebaixar administradores, configuração sensível, chaves, backup | Ser removido pela interface — só pelo CLI |
| **admin** | Configuração da instância, fontes, usuários, releases, migrações | Alterar o owner; ver chaves de assinatura |
| **moderator** | Denúncias, bloqueios, silenciamento, quarentena, aprovação de webmention | Ver configuração, chaves ou dados de conta |
| **user** | A própria conta | O resto |

A separação **moderator ≠ admin** é a que importa. Uma instância pública precisa
de vários moderadores e pouquíssimos administradores. Quase todo software
self-hosted funde os dois e obriga o operador a entregar as chaves do reino para
quem só ia apagar spam.

#### O que um administrador não pode fazer

Isto é decisão de produto, não só de segurança:

- **Não existe "entrar como usuário".** Impersonação é o backdoor favorito de
  painel administrativo e não vai existir aqui.
- Não lê rascunho não publicado de ninguém.
- Não recupera senha de ninguém — `argon2id` é irreversível por definição.
- **Toda ação administrativa entra no log de auditoria, inclusive as do owner.**
  Administrador não auditado é administrador em que ninguém confia.

#### Endurecimento da conta administrativa

- **2FA (TOTP) obrigatório** para `owner` e `admin` — não é preferência
- Sessão administrativa com TTL menor que a de usuário comum
- **Reautenticação** obrigatória para ação destrutiva: purgar conteúdo, remover
  conta, rotacionar chave, alterar papel
- Rate limit agressivo no login administrativo, contabilizado separadamente do
  login comum
- Painel administrativo em **porta separada** (`11090`, §4.2), ligado a
  `127.0.0.1` por padrão — alcançável **só por túnel SSH**. No Compose, a porta
  não é publicada. Para o operador paranoico, o painel simplesmente não existe
  na internet

#### Recuperação

O caminho de recuperação é o **CLI no servidor**:

```bash
./rss-social admin reset --email=voce@exemplo.org
```

Acesso ao servidor é a autoridade final. **Não existe recuperação de
administrador por e-mail** — um backdoor de e-mail para a conta mais poderosa da
instância é exatamente o vetor que compromete instâncias pequenas. Perder a
senha *e* o servidor significa perder a instância. Essa é a resposta honesta.

---

## 8. Experiência

### 8.1 Princípios

1. **Ler vem antes de postar.** O produto tem que ser bom para quem nunca vai responder.
2. **O domínio é a identidade mais forte.** Três estados, sempre visíveis e nunca confundidos: **conta local** · **site verificado** · **fonte acompanhada**. Um feed importado nunca ganha aparência de verificado só por ter nome e avatar.
3. **Tipos de conteúdo merecem apresentações diferentes.** Nota curta aparece inteira. Artigo mostra título, resumo, tempo de leitura. Podcast tem player e duração. Resposta mostra o trecho ao qual responde.
4. **Estado é conteúdo.** De onde veio, quando chegou, por qual caminho, qual versão, se a fonte está saudável. Visível, não escondido no admin.
5. **Nenhum aprisionamento.** OPML, Markdown, JSON Feed, mídia original, lista de bloqueios, banco inteiro. Excluir conta gera pacote antes de remover.
6. **Funciona sem JavaScript.** Não como cortesia — como arquitetura. SSR é o modo nativo; HTMX enriquece.

### 8.2 Navegação

**Hoje** · **Feed** · **Não lidos** · **Conversas** · **Salvos** · **Coleções** ·
**Descobrir** · **+ Publicar**

Desktop em três colunas, onde a terceira é uma **barra de contexto** (fonte
selecionada, conversa, percurso da resposta) — **não** um texto "Sobre" estático.
Celular em coluna única com cinco destinos na barra inferior.

### 8.3 Hoist / dehoist — emprestado do rss.chat, com crédito

Selecionar um post e "elevar" — a timeline some, sobra aquela conversa e suas
respostas. Elevar de novo desce mais um nível. Sair volta um nível por vez, com
o cursor onde estava.

É a melhor ideia de leitura de thread que encontrei na pesquisa e não estava no
documento original. Vale roubar, e vale creditar.

### 8.4 Saúde da fonte como superfície do leitor

O documento original colocava isso só no admin. Errado — quem lê também precisa:

> *Esta fonte não publica há 47 dias.*
> *Esta fonte redirecionou permanentemente para outro endereço.*
> *As últimas 3 leituras falharam (503).*

Com uma ação possível ao lado de cada uma. Feed morto silenciosamente é o
principal jeito de um leitor de RSS mentir para o usuário.

### 8.5 Aparência: "editorial instrumentado"

O documento original propôs uma direção de revista. Boa, mas incompleta para
este produto — porque o diferencial aqui é **transparência de estado**, e revista
não tem vocabulário para isso.

A direção proposta soma as duas:

- **Legibilidade de revista** para o corpo do texto — coluna de 65–75 caracteres, ≥16px, entrelinha 1,55–1,7
- **Instrumentação legível** para estado — cada item carrega uma faixa discreta de procedência (origem · quando chegou · por qual caminho · versão)
- **Três papéis tipográficos, não dois:** serifada para leitura · sem serifa para interface · **monoespaçada para estado técnico**. A monoespaçada é o que sinaliza "isto é fato do sistema, não opinião do design"
- **Ritmo por divisores e espaço**, não caixas em tudo. Cartão completo reservado para áudio/vídeo, citações, avisos operacionais e coleções
- **Densidade é escolha do usuário:** compacto · confortável · editorial
- **Claro e escuro, ambos intencionais.** Nem um é inversão do outro
- **Cor nunca é a única indicação.** Ícone e texto acompanham sempre
- **Movimento explica mudança de estado** e respeita `prefers-reduced-motion`. Item novo não desloca a leitura em curso

**Acessibilidade mínima:** WCAG 2.2 AA · navegação completa por teclado · foco
visível com dois indicadores além de cor · pular para o conteúdo · região viva
silenciosa para itens novos · zoom 200% sem perda de função.

*(A paleta concreta fica em aberto — ver §13.)*

---

## 9. Operação

### 9.1 Instalação

**Docker é o caminho oficial.** Um `compose.yaml`, um serviço:

```yaml
services:
  rss-social:
    image: ghcr.io/<org>/rss-social:0.1
    restart: unless-stopped
    ports:
      - "11080:11080"          # app — atrás de proxy reverso em produção
      # 11090 (admin) NÃO é publicada: acesso só por túnel SSH
    volumes:
      - ./data:/data           # banco SQLite, anexos, chaves
    environment:
      RSS_SOCIAL_DOMAIN: exemplo.org
      RSS_SOCIAL_LISTEN: ":11080"
      RSS_SOCIAL_ADMIN_LISTEN: "127.0.0.1:11090"
      RSS_SOCIAL_SMTP_URL: smtps://user:senha@smtp.exemplo.org:465
```

```bash
docker compose up -d
docker compose exec rss-social /rss-social admin create --email=voce@exemplo.org
```

O `init` roda sozinho no primeiro boot: gera chaves, cria o banco, aplica
migrações. O segundo comando imprime o link de setup do administrador (§7.5).

**Execução direta do binário continua suportada** — é o mesmo arquivo, sem
container — para desenvolvimento e para quem não quer Docker. Mas a imagem é o
artefato que documentamos, testamos e versionamos.

**Regras de container:**

- Roda como **usuário não-root** (`USER 65532`)
- Sistema de arquivos raiz **somente leitura**; só `/data` é gravável
- `cap_drop: [ALL]` — o processo não precisa de nenhuma capability
- **Nenhum estado fora de `/data`.** O container é descartável; o volume não
- `HEALTHCHECK` apontando para `/healthz`
- Imagem multi-arquitetura: `amd64` e `arm64` (VPS baratas e Raspberry Pi)

### 9.2 Comandos do binário

`serve` · `init` · `migrate` (com `--dry-run`) · `backup` · `restore` ·
`admin create` · `admin reset` · `admin list` (§7.5) ·
`doctor` (diagnóstico: banco, migrações, permissões, conectividade, filas
travadas) · `version`

### 9.3 Backup e restauração

Snapshot consistente do SQLite (`VACUUM INTO`, não cópia de arquivo aberto) +
anexos + configuração + chaves + **manifesto com versão de app e de schema** +
hash de cada arquivo.

**Regra:** backup só pode ser chamado de "funcional" depois de **restaurado
automaticamente num ambiente limpo durante o teste de release**. Backup não
testado não é backup.

### 9.4 Painel do operador

Responde rápido: quais fontes estão quebradas e por quê · quando cada uma
atualizou · último status HTTP · quais redirecionaram · quais são duplicatas ·
quais jobs estão pendentes, travados ou na dead-letter · quais webmentions
aguardam moderação · quanto espaço banco/cache/mídia ocupam · quando foi o
último backup **testado** · qual versão está instalada e se há migração pendente.

**Cada erro traz uma ação possível.** Tentar de novo · editar URL · aceitar
redirecionamento · pausar fonte · mesclar duplicata · ver detalhes.

### 9.5 Observabilidade

`log/slog` estruturado com ID de requisição · `/healthz` e `/readyz` ·
`/metrics` Prometheus (duração de ingestão, profundidade da fila, taxa de erro
por fonte, latência de entrega) · endpoint de debug protegido.

### 9.6 Releases

Versionamento semântico · changelog · migrações testadas nos dois sentidos ·
teste de restauração no pipeline de release · política de atualização ·
política de segurança e canal de divulgação de vulnerabilidade.

---

## 10. Roteiro

**Estimativas de planejamento, não compromissos.** Uma pessoa com apoio de
automação, partindo do zero em Go.

### Etapa 0 — Fundação e prova de interop · 1–2 semanas

Antes de qualquer feature: provar que o difícil funciona.

- Esqueleto do binário, config, migrações, `doctor`
- Guard SSRF **com testes de fuzzing**, incluindo caso de DNS rebinding
- Parse de RSS/Atom/JSON Feed contra o corpus real
- Emissão de RSS com namespace `source:` e **golden files**
- **O threadwalker do Dave caminhando uma conversa nossa, no CI**

**Saída:** um binário que não faz nada útil e prova que a parte difícil está certa.

### v0.1 — A espinha · 3–5 semanas

O mínimo que é inconfundivelmente *este* produto: post local e item remoto
convivendo na mesma timeline, e o laço fechando.

- Conta local, sessão, senha e link mágico
- Assinar fonte por URL, com descoberta a partir da página
- Ingestão com as três camadas (§5.2) e convergência determinística (§5.3)
- Timeline SSR única, com SSE
- Composição em Markdown, publicação local
- Feed RSS e JSON por usuário + firehose da instância
- Threading com `source:inReplyTo`, orfandade honesta e adoção
- Fila durável e livro-razão de entrega
- **Moderação básica: bloqueio, quarentena de fonte nova, rate limit, controle de cadastro**
- Backup e restauração **com teste automatizado**

**Portão:** duas instâncias federam entre si por RSS puro; e um `restore` num
servidor vazio passa no CI.

### v0.2 — Leitura séria · 3–4 semanas

Não lidos · salvos · coleções · busca FTS5 · filtros · densidades · tipos de
conteúdo com apresentações distintas · saúde da fonte visível · atalhos de
teclado · hoist/dehoist · importação e exportação OPML · o sistema visual completo

**Portão:** dá para usar diariamente como leitor principal, sem saudade de outro.

### v0.3 — Operação e moderação de verdade · 2–3 semanas

Painel completo · denúncias · silenciamento por palavra e domínio · registro de
decisões · métricas · releases versionados · migrações com rollback documentado ·
política pública

**Portão:** o operador consegue explicar e recuperar qualquer falha sozinho.

### v0.4 — Domínio como identidade

IndieAuth · descoberta de `h-card` e `rel="me"` · reivindicação e desvinculação
de domínio · múltiplos sites por conta · estados de verificação explícitos ·
perfil público com endpoints descobertos

### v0.5 — Conversa entre sites

Webmention (envio e recebimento, com moderação) · Micropub · a tela **Percurso** ·
atualização e exclusão idempotentes · suíte de interop expandida

### v0.6 — Mídia

Imagens e galerias · enclosures de áudio e vídeo · player acessível · cotas ·
remoção configurável de metadados sensíveis · backup incremental

### Depois

ActivityPub **como ponte, não como núcleo** · APIs documentadas para clientes
alternativos · PWA · diretórios externos

---

## 11. O que não construir

App nativo Android ou iOS · mensagens privadas criptografadas · chamadas de
áudio ou vídeo · marketplace de plugins · algoritmo de recomendação
comportamental · geração ou resumo por IA · microserviços · suporte simultâneo a
SQLite, PostgreSQL e MongoDB · CDN própria · monetização · federação ActivityPub
completa · promessa de escala massiva.

Cada item aumenta manutenção, superfície de ataque e suporte. Nenhum é
necessário para provar que um leitor social de feeds pode ser excelente.

---

## 12. Critérios de sucesso

### Experiência
Adicionar uma página e descobrir seu feed em menos de três ações · importar OPML
preservando pastas · achar não lidos, salvos e conversas sem treinamento ·
distinguir visualmente artigo, nota, resposta e podcast · operar as funções
centrais só com teclado · continuar lendo com JavaScript desligado · não perder a
posição quando chegam itens novos · **saber de onde veio cada item sem procurar**

### Correção
Duas instâncias com as mesmas observações em ordens diferentes convergem para o
**mesmo** item · reexecutar qualquer job não duplica nada · edição remota nunca
regride para versão anterior · o threadwalker do Dave caminha nossas conversas
sem alteração

### Operação
Subir numa VPS limpa com **um `compose.yaml` de um serviço** e nada mais
instalado além do Docker · atualizar trocando a tag da imagem, com migração
automática e backup anterior · restaurar num servidor vazio no teste de release ·
identificar fonte quebrada e o motivo pelo painel · exportar tudo sem ferramenta
proprietária

### Segurança
Bloquear SSRF conhecido, **incluindo via redirecionamento e DNS rebinding** ·
sanitizar a suíte de payloads XSS · limitar tamanho e tempo de download remoto ·
moderar webmentions antes de publicar quando configurado · nenhuma porta interna
exposta · processo documentado de divulgação de vulnerabilidade

### Desempenho e leveza (metas internas, medidas numa VPS definida)
Primeira resposta abaixo de **300 ms** no p95 para timeline indexada (o binário
único e o SSR permitem ser mais ambicioso que os 500 ms do documento original) ·
busca abaixo de 300 ms no p95 com 100 mil itens · ingerir 100 feeds heterogêneos
sem travar a interface · zero falhas críticas de contraste WCAG 2.2 AA · zero
perda de dados no teste automatizado de backup/restore

E o **orçamento de recursos de §4.4.1 respeitado e verificado no CI**:
< 40 MB ocioso · < 180 MB com 500 fontes · < 6% de um núcleo em regime ·
imagem < 40 MB · boot frio < 1 s · < 30 KB de JavaScript na página ·
**maioria das leituras de feed devolvendo `304`** (o indicador que mais
economiza CPU, banda e paciência alheia) · regressão de alocação no caminho
quente quebra o build

---

## 13. Questões em aberto — o que preciso decidir com você

1. **Licença.** MIT (máxima adoção, permite fork fechado) ou AGPL-3.0 (obriga
   quem hospedar modificado a abrir)? Para um projeto sobre não-aprisionamento,
   AGPL tem um argumento forte. Para um laboratório que quer que outros usem,
   MIT tem outro.

2. **Fatiamento do v0.1.** Proponho "a espinha" (publicar + ler + federar, tudo
   raso). A alternativa é "leitor primeiro, publicação no v0.2" — mais fiel ao
   princípio *ler antes de postar*, mas adia a prova de que o laço fecha.

3. **Paleta e tipografia.** A direção de §8.5 está definida; as cores concretas
   não. A paleta *Afluente* (teal/argila/musgo) era boa mas amarrada à metáfora
   do rio. Faço uma proposta nova, adapto aquela, ou você já tem preferência?

4. **Memória do driver SQLite.** `ncruces/go-sqlite3` é rápido e sem cgo, mas o
   sandbox WASM consome mais memória — e agora existe um orçamento explícito
   para ele estourar (§4.4.1). **Medir na Etapa 0**, junto do comportamento de
   `mmap_size` (§4.4.3), com plano B em `modernc.org/sqlite`. Não é decisão de
   agora, é medição de agora.

5. **Interop com o rss.chat.** Consumir os feeds dele é trivial e certo. Escutar
   o firehose WebSocket dele (`wss://rss.chat/`) é opcional — vale a
   complexidade extra no v0.1, ou fica para depois?

6. **Relação com o RSC.** Vale avisar o Ricardo que existe uma implementação
   independente em Go que estudou o trabalho dele? Custo zero, ganho provável em
   troca de casos de interop. Sua chamada.

---

## 14. Riscos conhecidos

| Risco | Mitigação |
|---|---|
| Reescrever do zero é mais lento que forkar | Aceito conscientemente; a Etapa 0 prova o difícil antes de investir no resto |
| Interop é frágil e quebra em silêncio | Golden files + threadwalker no CI + corpus versionado |
| Convergência determinística é conceitualmente difícil | É o principal valor de aprendizado; especificar e testar antes de codar |
| `ncruces/go-sqlite3` é menos maduro que o driver com cgo | Medir na Etapa 0; plano B pronto |
| Instância pública atrai abuso antes de haver moderação | Moderação no v0.1, não depois. Cadastro fechado até o v0.3 |
| Escopo cresce sozinho | §11 é vinculante. Feature fora dela exige revisão desta proposta |
| Um mantenedor só | Binário único e zero dependências de serviço reduzem custo operacional a quase nada |

---

## 15. Fontes

**Estudadas em código** (clonadas e lidas em 23/07/2026, depois removidas):
[rmdes/rsc](https://github.com/rmdes/rsc) ·
[scripting/rss.chat](https://github.com/scripting/rss.chat)

**Especificações:**
[RSS 2.0](https://www.rssboard.org/rss-specification) ·
[JSON Feed 1.1](https://www.jsonfeed.org/version/1.1/) ·
[OPML 2.0](https://opml.org/spec2.opml) ·
[WebSub](https://www.w3.org/TR/websub/) ·
[Webmention](https://www.w3.org/TR/webmention/) ·
[Micropub](https://www.w3.org/TR/micropub/) ·
[IndieAuth](https://indieauth.spec.indieweb.org/) ·
[microformats2](https://microformats.org/wiki/microformats2) ·
RFC 4685 (Atom Threading)

**Contexto Textcasting:**
[textcasting.org](https://textcasting.org/) ·
[The Future of RSS is Textcasting — kottke.org](https://kottke.org/23/11/the-future-of-rss-is-textcasting-1) ·
[source.scripting.com](https://source.scripting.com/)

**Ecossistema Go:**
[ncruces/go-sqlite3](https://github.com/ncruces/go-sqlite3) ·
[mmcdole/gofeed](https://github.com/mmcdole/gofeed) ·
[templ](https://templ.guide) ·
[willnorris.com/go/microformats](https://pkg.go.dev/willnorris.com/go/microformats) ·
[Go — IndieWeb](https://indieweb.org/Go) ·
[The Go Frontend Dilemma 2026](https://rajnandan.com/posts/go-frontend-architecture-2026/) ·
[SQLite driver benchmarks](https://github.com/ncruces/go-sqlite-bench)

---

### Nota de atualidade

Escrito em **23 de julho de 2026**. O RSC e o rss.chat receberam commits nesta
mesma data e estão em desenvolvimento acelerado — o RSC no meio de uma reescrita
de quatro verticais. Confirme o estado de ambos antes de tomar qualquer decisão
que dependa deles. As escolhas de biblioteca em Go foram verificadas hoje;
`go.hacdias.com/indielib` é a única marcada como **a verificar antes de adotar**.
