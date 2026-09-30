
# ---- git-stack git integration -------------------------------------------
# git's completion (git-completion.bash) calls _git_<subcommand> for external
# subcommands, and for aliases it resolves the alias to its first git word
# ("stack") and calls the same function. The alias tail ("submit --stack") is
# dropped there, but the typed alias name survives in COMP_WORDS, so this hook
# rebuilds the real command line and hands it to cobra's completion above.
_git_stack() {
    local idx="${__git_cmd_idx:-1}"
    local alias_name="${COMP_WORDS[idx]}"
    local -a tail=()
    if [[ "$alias_name" != "stack" ]]; then
        local expansion
        expansion=$(git config --get "alias.$alias_name" 2>/dev/null)
        case "$expansion" in
            stack)   ;;
            stack\ *) read -r -a tail <<<"${expansion#stack }" ;;
            *)       return 0 ;;
        esac
    fi
    local -a rest=()
    if (( cword > idx + 1 )); then
        rest=("${words[@]:idx+1:cword-idx-1}")
    fi
    local -a new_words=("git-stack" "${tail[@]}" "${rest[@]}" "$cur")
    COMP_WORDS=("${new_words[@]}")
    COMP_CWORD=$(( ${#new_words[@]} - 1 ))
    COMP_LINE="${new_words[*]}"
    COMP_POINT=${#COMP_LINE}
    # git's completion is registered with -o default; without candidates bash
    # would fall back to file names, which git-stack never wants unless the
    # directive asks for it (cobra's handler re-enables it in that case).
    compopt +o default 2>/dev/null
    __start_git-stack
}
