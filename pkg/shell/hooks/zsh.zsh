
# ---- git-stack git integration -------------------------------------------
# zsh's bundled _git expands aliases into $words before dispatching to
# _git-<subcommand>, so `git ss <TAB>` arrives here as
# words=(stack submit --stack …). cobra's function (renamed above) uses
# ${words[1]} as the binary, so point it at git-stack first.
_git-stack() {
    if [[ "${words[1]}" == "stack" ]]; then
        words[1]="git-stack"
    fi
    __git_stack_cobra "$@"
}

# git's own git-completion.zsh wrapper uses bash-style dispatch instead:
# _git_stack runs under `emulate ksh` with $words, $cword and $cur set and
# the alias name still in ${words[2]}.
_git_stack() {
    local alias_name="${words[2]}"
    local -a tail
    tail=()
    if [[ "$alias_name" != "stack" ]]; then
        local expansion
        expansion=$(git config --get "alias.$alias_name" 2>/dev/null)
        case "$expansion" in
            stack)    ;;
            stack\ *) tail=(${=${expansion#stack }}) ;;
            *)        return 0 ;;
        esac
    fi
    local -a rest
    rest=("${words[@]:2:$((cword-2))}")
    words=("git-stack" "${tail[@]}" "${rest[@]}" "$cur")
    CURRENT=${#words}
    __git_stack_cobra
}

compdef _git-stack git-stack

# When zsh autoloads this file, its whole body is the initial function call:
# the definitions above have just replaced it, so run the real function now.
if [[ "$funcstack[1]" == "_git-stack" ]]; then
    _git-stack "$@"
fi
