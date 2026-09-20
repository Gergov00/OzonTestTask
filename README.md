# GraphQL-сервис постов и комментариев

Сервис на Go для публикации постов и неограниченно вложенных комментариев. API поддерживает cursor-пагинацию, GraphQL Subscriptions и два взаимозаменяемых хранилища: in-memory и PostgreSQL 17.

## Возможности

- создание и чтение постов;
- включение и отключение комментариев владельцем поста;
- корневые комментарии и ответы с неограниченной глубиной дерева;
- cursor-пагинация постов, комментариев и ответов;
- доставка новых комментариев через GraphQL Subscriptions;
- выбор in-memory или PostgreSQL-хранилища при запуске;
- request-scoped DataLoader для защиты от N+1;
- ограничение сложности GraphQL-запросов и корректное завершение WebSocket-соединений.

## Быстрый старт

Требуются Go 1.25 или Docker с Docker Compose.

Запуск с in-memory-хранилищем:

```bash
go run ./cmd/server
```

После запуска доступны:

- GraphQL Playground: `http://localhost:8080/`;
- GraphQL HTTP/WebSocket: `http://localhost:8080/query`;
- проверка состояния: `http://localhost:8080/healthz`.

Запуск готового окружения с PostgreSQL:

```bash
docker compose --profile postgres up --build app-postgres
```

В этом режиме API доступен на `http://localhost:8081`.

## Архитектура

Поток запроса разделён на независимые слои:

1. HTTP/WebSocket transport принимает GraphQL-запрос и извлекает автора.
2. Прикладной сервис проверяет входные данные, права владельца и бизнес-правила.
3. Общий интерфейс репозитория скрывает in-memory и PostgreSQL-реализации.
4. Request-scoped DataLoader пакетно загружает комментарии и устраняет N+1.
5. Локальный брокер отправляет сохранённые комментарии WebSocket-подписчикам.

`authorID` не передаётся в GraphQL-мутациях. Доверенный внешний компонент должен записать непрозрачный идентификатор текущего пользователя в заголовок `X-Author-ID`. В этом тестовом сервисе отдельная аутентификация, пользователи, пароли и JWT намеренно не реализованы.

## Структура проекта

```text
cmd/server/                    точка входа и HTTP/WebSocket-сервер
graph/                         GraphQL-схема, модели и резолверы gqlgen
internal/auth/                 извлечение AuthorID из HTTP-контекста
internal/config/               конфигурация из переменных окружения
internal/domain/               доменные модели и ошибки
internal/graphqlapi/           представление GraphQL-ошибок
internal/loader/               request-scoped DataLoader комментариев
internal/repository/memory/    in-memory-хранилище
internal/repository/postgres/  PostgreSQL-хранилище и миграции
internal/service/              бизнес-правила приложения
internal/subscription/         брокер событий комментариев
```

## Переменные окружения

| Переменная | По умолчанию | Назначение |
|---|---:|---|
| `HTTP_ADDR` | `:8080` | Адрес HTTP-сервера внутри процесса |
| `STORAGE_DRIVER` | `memory` | Хранилище: `memory` или `postgres` |
| `DATABASE_URL` | — | Строка подключения; обязательна для `postgres` |

Compose дополнительно читает `MEMORY_HTTP_HOST`, `MEMORY_HTTP_PORT`, `POSTGRES_HTTP_HOST`, `POSTGRES_HTTP_PORT`, `POSTGRES_HOST`, `POSTGRES_PORT`, `POSTGRES_DB`, `POSTGRES_USER` и `POSTGRES_PASSWORD`. Все опубликованные порты по умолчанию привязаны только к `127.0.0.1`. Не меняйте host-переменные на внешний интерфейс без явно настроенного сетевого доступа и аутентификации. Файл [.env.example](.env.example) содержит только демонстрационные локальные значения. Они непригодны для production; реальные секреты храните вне репозитория.

## Локальный запуск с Go

Требуется Go 1.25.

```bash
go run ./cmd/server
```

По умолчанию используется in-memory-хранилище и адрес `http://localhost:8080`. Для PostgreSQL:

```bash
STORAGE_DRIVER=postgres \
DATABASE_URL='postgres://testozon:testozon-local-only@localhost:5432/testozon?sslmode=disable' \
go run ./cmd/server
```

## Запуск в Docker

Профили независимы и используют разные host-порты, поэтому их можно запускать одновременно без конфликта. Образ приложения в обоих режимах один и запускается непривилегированным пользователем distroless.

In-memory на `http://localhost:8080`:

```bash
docker compose --profile memory up --build app
```

PostgreSQL 17 на `http://localhost:8081`:

```bash
docker compose --profile postgres up --build app-postgres
```

PostgreSQL ожидается через `pg_isready` до запуска приложения. Данные находятся в named volume. Чтобы остановить сервисы, сохранив данные, выполните `docker compose down`. Для удаления локальных данных именно этого Compose-проекта используйте `docker compose down -v`.

В distroless-образе нет shell, `curl` или `wget`, поэтому контейнер приложения намеренно не содержит фиктивный `HEALTHCHECK`. Доступность проверяется с хоста через `GET /healthz`; готовность PostgreSQL проверяет Compose.

## API и Playground

- Playground: `http://localhost:8080/` (или порт `8081` в postgres-профиле).
- GraphQL HTTP/WebSocket endpoint: `/query`.
- Liveness endpoint: `GET /healthz`.

Для операций записи добавьте HTTP-заголовок:

```json
{
  "X-Author-ID": "author-1"
}
```

### Создание поста

```graphql
mutation CreatePost {
  createPost(input: {title: "Первый пост", content: "Текст поста"}) {
    id
    authorID
    title
    commentsEnabled
    createdAt
  }
}
```

Пример через HTTP:

```bash
curl http://localhost:8080/query \
  -H 'Content-Type: application/json' \
  -H 'X-Author-ID: author-1' \
  --data '{"query":"mutation { createPost(input: {title: \"Первый пост\", content: \"Текст поста\"}) { id authorID title commentsEnabled } }"}'
```

### Создание комментария или ответа

Корневой комментарий:

```graphql
mutation CreateComment($postID: ID!) {
  createComment(input: {postID: $postID, text: "Корневой комментарий"}) {
    id
    postID
    parentID
    authorID
    text
  }
}
```

Для ответа укажите `parentID`:

```graphql
mutation CreateReply($postID: ID!, $parentID: ID!) {
  createComment(input: {
    postID: $postID
    parentID: $parentID
    text: "Ответ"
  }) {
    id
    parentID
    text
  }
}
```

Обе мутации требуют `X-Author-ID`.

### Включение и отключение комментариев

Операцию может выполнить только автор поста, указанный в `X-Author-ID`:

```graphql
mutation SetComments($postID: ID!) {
  setPostCommentsEnabled(postID: $postID, enabled: false) {
    id
    commentsEnabled
  }
}
```

### Пагинация

`first` должен быть от 1 до 100. `endCursor` передаётся как `after` следующего запроса и рассматривается клиентом как непрозрачная строка.

```graphql
query Posts($after: String) {
  posts(first: 10, after: $after) {
    nodes {
      id
      title
      comments(first: 5) {
        nodes {
          id
          text
          replies(first: 5) {
            nodes { id text }
            pageInfo { hasNextPage endCursor }
          }
        }
        pageInfo { hasNextPage endCursor }
      }
    }
    pageInfo { hasNextPage endCursor }
  }
}
```

Для первой страницы передайте `{"after": null}`, для следующей — значение `pageInfo.endCursor`.

### Подписка на комментарии

Подключитесь к WebSocket endpoint `/query` с протоколом `graphql-transport-ws`:

```graphql
subscription CommentAdded($postID: ID!) {
  commentAdded(postID: $postID) {
    id
    postID
    parentID
    authorID
    text
    createdAt
  }
}
```

События доставляются только подписчикам указанного поста и только после успешного сохранения комментария.

## Тестирование

Полная локальная проверка:

```bash
go generate ./...
go mod verify
go vet ./...
go test ./...
go test -race ./...
```

Интеграционный контракт PostgreSQL имеет build tag `integration` и требует `TEST_DATABASE_URL` с отдельной тестовой базой:

```bash
TEST_DATABASE_URL='postgres://testozon:testozon-local-only@localhost:5432/testozon?sslmode=disable' \
go test -tags=integration -race ./internal/repository/postgres -v
```

## Ограничения

- In-memory-данные теряются после перезапуска.
- Подписки хранятся в процессе и работают только внутри одного экземпляра приложения. Для горизонтального масштабирования нужен внешний брокер сообщений.
- `X-Author-ID` считается проверенным upstream-компонентом; прямой публичный доступ без такого компонента небезопасен.
- Глубина дерева не ограничена схемой, но каждый явно запрошенный уровень влияет на GraphQL complexity limit.
