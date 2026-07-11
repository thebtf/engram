# DB-TEST-POOL-HYGIENE R4 — независимый checker

Вердикт: **ACCEPT**

`READY_FOR_INTEGRATION=true` для evidence-only commit
`ce6a40d72fc39932ccbc4b949647f321b91f70c3`, с обязательным последующим
root/PM post-run review перед синтезом.

CRITICAL/HIGH findings: **0**.

## Точная граница

- Target: `ce6a40d72fc39932ccbc4b949647f321b91f70c3`
- Parent: `331b5b195a967e7f27dca94038a3480c9afcc84f`
- Tree: `20a69d7a83e0100de7e3e73e670fe02feadada2d`
- Maker delta: ровно 13 путей, все обычные Git blobs mode `100644`
- Ordinal + terminal-LF SHA-256 списка путей:
  `8385cf487753e79ef3419a66e58a0bac1592bdaca2fc1d4d9022a0946eeb8f5a`
- Maker boundary перед проверкой был чистым.
- Product/test/spec/plan/register/HTML maker не менял; checker меняет только
  `.agent/reviews/db-test-pool-hygiene-r4-fresh-checker/**`.

Product-regression scan: **PASS**. R4 — evidence-only revision, а product blob
`internal/db/gorm/candidate_store_test.go` побайтово совпадает с принятым
candidate: Git OID `7337f1bd8da4fb315de842eea2e3cce5476250a3`, 47 016 bytes,
SHA-256 `62260c1a2e0705b065295322dd23fcf9b17fd47cb5ebc64134630788e2d23e09`.

## Проверка закрытия R3 findings

Verifier в `GitRevision HEAD` и `GitIndex` независимо вернул PASS с одинаковой
реальностью: 21 changed path, 19 directly bound, 35 manifest entries, 36
checksum entries, inventory 83/8.

Maker harness полностью воспроизведён: 35/35 coherent alternate-index cases
PASS, failed cases отсутствуют, case-order SHA-256
`0fcc7c53259982de592100e249bc323efd4406cc9b7905627afa963b771099f8`, temp
root удалён.

Checker-owned harness не вызывает maker harness и строит собственный свежий
repository/index для каждого кейса. Итог: 28/28 PASS. Все пять прежних
false-green классов теперь fail closed с ожидаемым diagnostic:

1. `unknown_manifest_key`
2. `unknown_inventory_key`
3. `inventory_schema_99`
4. `stale_adversarial_proof`
5. `stale_verifier_proof`

Соседние атаки также закрыты: case-sensitive keys/paths, schema/type/array/count,
entry и outer digests, inventory lines/duplicates, manifest traversal, foreign
index path, incomplete и unbound checksum, non-empty/false proof objects и
product-blob mutation.

## Детерминизм и gates

- Seed дважды дал одинаковые adversarial/verifier proof bytes и не изменил
  manifest/checksum.
- Builder дважды воспроизвёл committed manifest
  `b0b63988722207186805712b1f32d80ee81afaacffa57e54144d5bd82c5dcb6c`
  и checksum
  `e8e2bcc519b9488b0b84628e62931175a2e210cac3ca57b2e2e999aa07538043`
  побайтово.
- PowerShell parse: 7 scripts, 0 errors.
- `git diff --check`: PASS.
- Gitleaks 8.30.0: 1 exact maker commit, 0 leaks.
- `go build ./...`: PASS.
- `go vet ./...`: PASS.
- Focused DB regression: PASS `count=5`.
- Focused DB regression with race detector: PASS `count=1`.
- После каждого Go-процесса checker DB sessions: 0.
- Финальный checker DB/activity/temp residue: 0/0/0.

Полные результаты: `gates.json`, `independent-attacks.json` и
`builder-determinism.json` в этой директории.

## Findings и concerns

Blocking findings отсутствуют.

Неблокирующие наблюдения:

- Fresh-DB migration печатает существующие нефатальные сообщения о legacy
  index columns/tables и недоступном `vectorscale`; pool-ownership invariant
  при этом проходит пять повторов и race detector. R4 не меняет этот product
  path, поэтому это не finding данного evidence-only diff, но шум не скрыт.
- Полные 35- и 28-case harnesses дороги по времени: каждый кейс создаёт свежий
  repo/index и отдельный verifier process. Это цена сильной изоляции, не дефект
  корректности.

Checker execution дважды сам себя скорректировал и после каждой коррекции
повторил соответствующий gate целиком: no-op case-variant fixture и неверная
PowerShell-интерполяция DSN. Обе ошибочные временные БД/директории удалены;
финальный residue scan пуст.

Reusability candidates: none — evaluated; packet-specific verifier evidence.

## Finish state

`review-needed`: checker принимает maker commit; следующий владелец — root/PM
post-run review и только затем синтез принятой головы. Checker не выполнял
merge, push, tag или внешнюю мутацию.

LITE_REVIEW_DONE: ACCEPT
