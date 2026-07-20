#!/bin/sh
# Claude Code hooks から GM0-typing へAIの状態を通知する（herdr 外での連携用）。
#
# 使い方: claude-hook.sh working|blocked|done
# 保存先は GM0-typing の --status-file と同じ場所（GM0_TYPING_STATUS_FILE で変更可能）。
STATUS="${1:-working}"
FILE="${GM0_TYPING_STATUS_FILE:-$HOME/.local/share/gm0-typing/status.json}"
mkdir -p "$(dirname "$FILE")"
cat > /dev/null # hooks が渡す stdin の JSON は読み捨てる
printf '{"agents":[{"name":"claude","status":"%s"}]}\n' "$STATUS" > "$FILE.tmp" \
  && mv "$FILE.tmp" "$FILE"
