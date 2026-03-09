find . -type f -name "go.mod" -print0 | while IFS= read -r -d $'\0' file; do
  (
    cd "$(dirname "$file")" || exit 1
    echo "Tidying module in: $(pwd)"
    go mod tidy
  )
done