# ---- git-stack git integration -------------------------------------------
# fish's git completion delegates `git stack …` (and any alias whose first
# word is stack) to `complete -C "git-stack <args>"`, dropping the rest of the
# alias. It leaves the typed subcommand in $__fish_git_cmd, so rebuild the
# real argument list here before asking git-stack for completions.
function __git_stack_git_args
    set -l args $argv
    if set -q __fish_git_cmd; and test -n "$__fish_git_cmd"; and test "$__fish_git_cmd" != stack
        if string match -q -- "git $__fish_git_cmd*" "$__fish_git_cmdline"
            set -l expansion (command git config --get "alias.$__fish_git_cmd" 2>/dev/null)
            if string match -q -- 'stack *' "$expansion"
                set -l tail (string split ' ' -- $expansion)[2..]
                set args $args[1] $tail $args[2..]
            end
        end
        # Forget the git context so a direct `git-stack <TAB>` later is not
        # mistaken for the alias.
        set -e __fish_git_cmd
        set -e __fish_git_cmdline
    end
    printf '%s\n' $args
end

