# bash completion for teams                                -*- shell-script -*-

__teams_debug()
{
    if [[ -n ${BASH_COMP_DEBUG_FILE:-} ]]; then
        echo "$*" >> "${BASH_COMP_DEBUG_FILE}"
    fi
}

# Homebrew on Macs have version 1.3 of bash-completion which doesn't include
# _init_completion. This is a very minimal version of that function.
__teams_init_completion()
{
    COMPREPLY=()
    _get_comp_words_by_ref "$@" cur prev words cword
}

__teams_index_of_word()
{
    local w word=$1
    shift
    index=0
    for w in "$@"; do
        [[ $w = "$word" ]] && return
        index=$((index+1))
    done
    index=-1
}

__teams_contains_word()
{
    local w word=$1; shift
    for w in "$@"; do
        [[ $w = "$word" ]] && return
    done
    return 1
}

__teams_handle_go_custom_completion()
{
    __teams_debug "${FUNCNAME[0]}: cur is ${cur}, words[*] is ${words[*]}, #words[@] is ${#words[@]}"

    local shellCompDirectiveError=1
    local shellCompDirectiveNoSpace=2
    local shellCompDirectiveNoFileComp=4
    local shellCompDirectiveFilterFileExt=8
    local shellCompDirectiveFilterDirs=16

    local out requestComp lastParam lastChar comp directive args

    # Prepare the command to request completions for the program.
    # Calling ${words[0]} instead of directly teams allows handling aliases
    args=("${words[@]:1}")
    # Disable ActiveHelp which is not supported for bash completion v1
    requestComp="TEAMS_ACTIVE_HELP=0 ${words[0]} __completeNoDesc ${args[*]}"

    lastParam=${words[$((${#words[@]}-1))]}
    lastChar=${lastParam:$((${#lastParam}-1)):1}
    __teams_debug "${FUNCNAME[0]}: lastParam ${lastParam}, lastChar ${lastChar}"

    if [ -z "${cur}" ] && [ "${lastChar}" != "=" ]; then
        # If the last parameter is complete (there is a space following it)
        # We add an extra empty parameter so we can indicate this to the go method.
        __teams_debug "${FUNCNAME[0]}: Adding extra empty parameter"
        requestComp="${requestComp} \"\""
    fi

    __teams_debug "${FUNCNAME[0]}: calling ${requestComp}"
    # Use eval to handle any environment variables and such
    out=$(eval "${requestComp}" 2>/dev/null)

    # Extract the directive integer at the very end of the output following a colon (:)
    directive=${out##*:}
    # Remove the directive
    out=${out%:*}
    if [ "${directive}" = "${out}" ]; then
        # There is not directive specified
        directive=0
    fi
    __teams_debug "${FUNCNAME[0]}: the completion directive is: ${directive}"
    __teams_debug "${FUNCNAME[0]}: the completions are: ${out}"

    if [ $((directive & shellCompDirectiveError)) -ne 0 ]; then
        # Error code.  No completion.
        __teams_debug "${FUNCNAME[0]}: received error from custom completion go code"
        return
    else
        if [ $((directive & shellCompDirectiveNoSpace)) -ne 0 ]; then
            if [[ $(type -t compopt) = "builtin" ]]; then
                __teams_debug "${FUNCNAME[0]}: activating no space"
                compopt -o nospace
            fi
        fi
        if [ $((directive & shellCompDirectiveNoFileComp)) -ne 0 ]; then
            if [[ $(type -t compopt) = "builtin" ]]; then
                __teams_debug "${FUNCNAME[0]}: activating no file completion"
                compopt +o default
            fi
        fi
    fi

    if [ $((directive & shellCompDirectiveFilterFileExt)) -ne 0 ]; then
        # File extension filtering
        local fullFilter filter filteringCmd
        # Do not use quotes around the $out variable or else newline
        # characters will be kept.
        for filter in ${out}; do
            fullFilter+="$filter|"
        done

        filteringCmd="_filedir $fullFilter"
        __teams_debug "File filtering command: $filteringCmd"
        $filteringCmd
    elif [ $((directive & shellCompDirectiveFilterDirs)) -ne 0 ]; then
        # File completion for directories only
        local subdir
        # Use printf to strip any trailing newline
        subdir=$(printf "%s" "${out}")
        if [ -n "$subdir" ]; then
            __teams_debug "Listing directories in $subdir"
            __teams_handle_subdirs_in_dir_flag "$subdir"
        else
            __teams_debug "Listing directories in ."
            _filedir -d
        fi
    else
        while IFS='' read -r comp; do
            COMPREPLY+=("$comp")
        done < <(compgen -W "${out}" -- "$cur")
    fi
}

__teams_handle_reply()
{
    __teams_debug "${FUNCNAME[0]}"
    local comp
    case $cur in
        -*)
            if [[ $(type -t compopt) = "builtin" ]]; then
                compopt -o nospace
            fi
            local allflags
            if [ ${#must_have_one_flag[@]} -ne 0 ]; then
                allflags=("${must_have_one_flag[@]}")
            else
                allflags=("${flags[*]} ${two_word_flags[*]}")
            fi
            while IFS='' read -r comp; do
                COMPREPLY+=("$comp")
            done < <(compgen -W "${allflags[*]}" -- "$cur")
            if [[ $(type -t compopt) = "builtin" ]]; then
                [[ "${COMPREPLY[0]}" == *= ]] || compopt +o nospace
            fi

            # complete after --flag=abc
            if [[ $cur == *=* ]]; then
                if [[ $(type -t compopt) = "builtin" ]]; then
                    compopt +o nospace
                fi

                local index flag
                flag="${cur%=*}"
                __teams_index_of_word "${flag}" "${flags_with_completion[@]}"
                COMPREPLY=()
                if [[ ${index} -ge 0 ]]; then
                    PREFIX=""
                    cur="${cur#*=}"
                    ${flags_completion[${index}]}
                    if [ -n "${ZSH_VERSION:-}" ]; then
                        # zsh completion needs --flag= prefix
                        eval "COMPREPLY=( \"\${COMPREPLY[@]/#/${flag}=}\" )"
                    fi
                fi
            fi

            if [[ -z "${flag_parsing_disabled}" ]]; then
                # If flag parsing is enabled, we have completed the flags and can return.
                # If flag parsing is disabled, we may not know all (or any) of the flags, so we fallthrough
                # to possibly call handle_go_custom_completion.
                return 0;
            fi
            ;;
    esac

    # check if we are handling a flag with special work handling
    local index
    __teams_index_of_word "${prev}" "${flags_with_completion[@]}"
    if [[ ${index} -ge 0 ]]; then
        ${flags_completion[${index}]}
        return
    fi

    # we are parsing a flag and don't have a special handler, no completion
    if [[ ${cur} != "${words[cword]}" ]]; then
        return
    fi

    local completions
    completions=("${commands[@]}")
    if [[ ${#must_have_one_noun[@]} -ne 0 ]]; then
        completions+=("${must_have_one_noun[@]}")
    elif [[ -n "${has_completion_function}" ]]; then
        # if a go completion function is provided, defer to that function
        __teams_handle_go_custom_completion
    fi
    if [[ ${#must_have_one_flag[@]} -ne 0 ]]; then
        completions+=("${must_have_one_flag[@]}")
    fi
    while IFS='' read -r comp; do
        COMPREPLY+=("$comp")
    done < <(compgen -W "${completions[*]}" -- "$cur")

    if [[ ${#COMPREPLY[@]} -eq 0 && ${#noun_aliases[@]} -gt 0 && ${#must_have_one_noun[@]} -ne 0 ]]; then
        while IFS='' read -r comp; do
            COMPREPLY+=("$comp")
        done < <(compgen -W "${noun_aliases[*]}" -- "$cur")
    fi

    if [[ ${#COMPREPLY[@]} -eq 0 ]]; then
        if declare -F __teams_custom_func >/dev/null; then
            # try command name qualified custom func
            __teams_custom_func
        else
            # otherwise fall back to unqualified for compatibility
            declare -F __custom_func >/dev/null && __custom_func
        fi
    fi

    # available in bash-completion >= 2, not always present on macOS
    if declare -F __ltrim_colon_completions >/dev/null; then
        __ltrim_colon_completions "$cur"
    fi

    # If there is only 1 completion and it is a flag with an = it will be completed
    # but we don't want a space after the =
    if [[ "${#COMPREPLY[@]}" -eq "1" ]] && [[ $(type -t compopt) = "builtin" ]] && [[ "${COMPREPLY[0]}" == --*= ]]; then
       compopt -o nospace
    fi
}

# The arguments should be in the form "ext1|ext2|extn"
__teams_handle_filename_extension_flag()
{
    local ext="$1"
    _filedir "@(${ext})"
}

__teams_handle_subdirs_in_dir_flag()
{
    local dir="$1"
    pushd "${dir}" >/dev/null 2>&1 && _filedir -d && popd >/dev/null 2>&1 || return
}

__teams_handle_flag()
{
    __teams_debug "${FUNCNAME[0]}: c is $c words[c] is ${words[c]}"

    # if a command required a flag, and we found it, unset must_have_one_flag()
    local flagname=${words[c]}
    local flagvalue=""
    # if the word contained an =
    if [[ ${words[c]} == *"="* ]]; then
        flagvalue=${flagname#*=} # take in as flagvalue after the =
        flagname=${flagname%=*} # strip everything after the =
        flagname="${flagname}=" # but put the = back
    fi
    __teams_debug "${FUNCNAME[0]}: looking for ${flagname}"
    if __teams_contains_word "${flagname}" "${must_have_one_flag[@]}"; then
        must_have_one_flag=()
    fi

    # if you set a flag which only applies to this command, don't show subcommands
    if __teams_contains_word "${flagname}" "${local_nonpersistent_flags[@]}"; then
      commands=()
    fi

    # keep flag value with flagname as flaghash
    # flaghash variable is an associative array which is only supported in bash > 3.
    if [[ -z "${BASH_VERSION:-}" || "${BASH_VERSINFO[0]:-}" -gt 3 ]]; then
        if [ -n "${flagvalue}" ] ; then
            flaghash[${flagname}]=${flagvalue}
        elif [ -n "${words[ $((c+1)) ]}" ] ; then
            flaghash[${flagname}]=${words[ $((c+1)) ]}
        else
            flaghash[${flagname}]="true" # pad "true" for bool flag
        fi
    fi

    # skip the argument to a two word flag
    if [[ ${words[c]} != *"="* ]] && __teams_contains_word "${words[c]}" "${two_word_flags[@]}"; then
        __teams_debug "${FUNCNAME[0]}: found a flag ${words[c]}, skip the next argument"
        c=$((c+1))
        # if we are looking for a flags value, don't show commands
        if [[ $c -eq $cword ]]; then
            commands=()
        fi
    fi

    c=$((c+1))

}

__teams_handle_noun()
{
    __teams_debug "${FUNCNAME[0]}: c is $c words[c] is ${words[c]}"

    if __teams_contains_word "${words[c]}" "${must_have_one_noun[@]}"; then
        must_have_one_noun=()
    elif __teams_contains_word "${words[c]}" "${noun_aliases[@]}"; then
        must_have_one_noun=()
    fi

    nouns+=("${words[c]}")
    c=$((c+1))
}

__teams_handle_command()
{
    __teams_debug "${FUNCNAME[0]}: c is $c words[c] is ${words[c]}"

    local next_command
    if [[ -n ${last_command} ]]; then
        next_command="_${last_command}_${words[c]//:/__}"
    else
        if [[ $c -eq 0 ]]; then
            next_command="_teams_root_command"
        else
            next_command="_${words[c]//:/__}"
        fi
    fi
    c=$((c+1))
    __teams_debug "${FUNCNAME[0]}: looking for ${next_command}"
    declare -F "$next_command" >/dev/null && $next_command
}

__teams_handle_word()
{
    if [[ $c -ge $cword ]]; then
        __teams_handle_reply
        return
    fi
    __teams_debug "${FUNCNAME[0]}: c is $c words[c] is ${words[c]}"
    if [[ "${words[c]}" == -* ]]; then
        __teams_handle_flag
    elif __teams_contains_word "${words[c]}" "${commands[@]}"; then
        __teams_handle_command
    elif [[ $c -eq 0 ]]; then
        __teams_handle_command
    elif __teams_contains_word "${words[c]}" "${command_aliases[@]}"; then
        # aliashash variable is an associative array which is only supported in bash > 3.
        if [[ -z "${BASH_VERSION:-}" || "${BASH_VERSINFO[0]:-}" -gt 3 ]]; then
            words[c]=${aliashash[${words[c]}]}
            __teams_handle_command
        else
            __teams_handle_noun
        fi
    else
        __teams_handle_noun
    fi
    __teams_handle_word
}

_teams_alias_help()
{
    last_command="teams_alias_help"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    has_completion_function=1
    noun_aliases=()
}

_teams_alias_list()
{
    last_command="teams_alias_list"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_alias_rm()
{
    last_command="teams_alias_rm"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_alias_set()
{
    last_command="teams_alias_set"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_alias()
{
    last_command="teams_alias"

    command_aliases=()

    commands=()
    commands+=("help")
    commands+=("list")
    commands+=("rm")
    if [[ -z "${BASH_VERSION:-}" || "${BASH_VERSINFO[0]:-}" -gt 3 ]]; then
        command_aliases+=("remove")
        aliashash["remove"]="rm"
    fi
    commands+=("set")

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_api()
{
    last_command="teams_api"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--dry-run")
    local_nonpersistent_flags+=("--dry-run")
    flags+=("--field=")
    two_word_flags+=("--field")
    two_word_flags+=("-f")
    local_nonpersistent_flags+=("--field")
    local_nonpersistent_flags+=("--field=")
    local_nonpersistent_flags+=("-f")
    flags+=("--header=")
    two_word_flags+=("--header")
    local_nonpersistent_flags+=("--header")
    local_nonpersistent_flags+=("--header=")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--input=")
    two_word_flags+=("--input")
    local_nonpersistent_flags+=("--input")
    local_nonpersistent_flags+=("--input=")
    flags+=("--query=")
    two_word_flags+=("--query")
    local_nonpersistent_flags+=("--query")
    local_nonpersistent_flags+=("--query=")
    flags+=("--raw-field=")
    two_word_flags+=("--raw-field")
    two_word_flags+=("-F")
    local_nonpersistent_flags+=("--raw-field")
    local_nonpersistent_flags+=("--raw-field=")
    local_nonpersistent_flags+=("-F")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_auth_help()
{
    last_command="teams_auth_help"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    has_completion_function=1
    noun_aliases=()
}

_teams_auth_login()
{
    last_command="teams_auth_login"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--device")
    local_nonpersistent_flags+=("--device")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--scopes=")
    two_word_flags+=("--scopes")
    local_nonpersistent_flags+=("--scopes")
    local_nonpersistent_flags+=("--scopes=")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_auth_logout()
{
    last_command="teams_auth_logout"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--all")
    local_nonpersistent_flags+=("--all")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--yes")
    local_nonpersistent_flags+=("--yes")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_auth_status()
{
    last_command="teams_auth_status"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--admin-request")
    local_nonpersistent_flags+=("--admin-request")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_auth()
{
    last_command="teams_auth"

    command_aliases=()

    commands=()
    commands+=("help")
    commands+=("login")
    commands+=("logout")
    commands+=("status")

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_cache_clear()
{
    last_command="teams_cache_clear"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--ai")
    local_nonpersistent_flags+=("--ai")
    flags+=("--all")
    local_nonpersistent_flags+=("--all")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--names")
    local_nonpersistent_flags+=("--names")
    flags+=("--yes")
    local_nonpersistent_flags+=("--yes")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_cache_help()
{
    last_command="teams_cache_help"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    has_completion_function=1
    noun_aliases=()
}

_teams_cache_info()
{
    last_command="teams_cache_info"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_cache()
{
    last_command="teams_cache"

    command_aliases=()

    commands=()
    commands+=("clear")
    commands+=("help")
    commands+=("info")

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_channel_files()
{
    last_command="teams_channel_files"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--team=")
    two_word_flags+=("--team")
    local_nonpersistent_flags+=("--team")
    local_nonpersistent_flags+=("--team=")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_channel_help()
{
    last_command="teams_channel_help"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    has_completion_function=1
    noun_aliases=()
}

_teams_channel_list()
{
    last_command="teams_channel_list"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--team=")
    two_word_flags+=("--team")
    local_nonpersistent_flags+=("--team")
    local_nonpersistent_flags+=("--team=")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_channel_read()
{
    last_command="teams_channel_read"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--all")
    local_nonpersistent_flags+=("--all")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--limit=")
    two_word_flags+=("--limit")
    local_nonpersistent_flags+=("--limit")
    local_nonpersistent_flags+=("--limit=")
    flags+=("--replies")
    local_nonpersistent_flags+=("--replies")
    flags+=("--since=")
    two_word_flags+=("--since")
    local_nonpersistent_flags+=("--since")
    local_nonpersistent_flags+=("--since=")
    flags+=("--team=")
    two_word_flags+=("--team")
    local_nonpersistent_flags+=("--team")
    local_nonpersistent_flags+=("--team=")
    flags+=("--until=")
    two_word_flags+=("--until")
    local_nonpersistent_flags+=("--until")
    local_nonpersistent_flags+=("--until=")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_channel_show()
{
    last_command="teams_channel_show"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--team=")
    two_word_flags+=("--team")
    local_nonpersistent_flags+=("--team")
    local_nonpersistent_flags+=("--team=")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_channel()
{
    last_command="teams_channel"

    command_aliases=()

    commands=()
    commands+=("files")
    commands+=("help")
    commands+=("list")
    commands+=("read")
    commands+=("show")

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_chat_add-member()
{
    last_command="teams_chat_add-member"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--dry-run")
    local_nonpersistent_flags+=("--dry-run")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_chat_create()
{
    last_command="teams_chat_create"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--dry-run")
    local_nonpersistent_flags+=("--dry-run")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--topic=")
    two_word_flags+=("--topic")
    local_nonpersistent_flags+=("--topic")
    local_nonpersistent_flags+=("--topic=")
    flags+=("--with=")
    two_word_flags+=("--with")
    local_nonpersistent_flags+=("--with")
    local_nonpersistent_flags+=("--with=")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_chat_delete()
{
    last_command="teams_chat_delete"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--dry-run")
    local_nonpersistent_flags+=("--dry-run")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--yes")
    local_nonpersistent_flags+=("--yes")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_chat_help()
{
    last_command="teams_chat_help"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    has_completion_function=1
    noun_aliases=()
}

_teams_chat_list()
{
    last_command="teams_chat_list"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--all")
    local_nonpersistent_flags+=("--all")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--limit=")
    two_word_flags+=("--limit")
    local_nonpersistent_flags+=("--limit")
    local_nonpersistent_flags+=("--limit=")
    flags+=("--topic=")
    two_word_flags+=("--topic")
    local_nonpersistent_flags+=("--topic")
    local_nonpersistent_flags+=("--topic=")
    flags+=("--unread")
    local_nonpersistent_flags+=("--unread")
    flags+=("--with=")
    two_word_flags+=("--with")
    local_nonpersistent_flags+=("--with")
    local_nonpersistent_flags+=("--with=")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_chat_mark-read()
{
    last_command="teams_chat_mark-read"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--dry-run")
    local_nonpersistent_flags+=("--dry-run")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_chat_mark-unread()
{
    last_command="teams_chat_mark-unread"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--dry-run")
    local_nonpersistent_flags+=("--dry-run")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_chat_read()
{
    last_command="teams_chat_read"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--all")
    local_nonpersistent_flags+=("--all")
    flags+=("--from=")
    two_word_flags+=("--from")
    local_nonpersistent_flags+=("--from")
    local_nonpersistent_flags+=("--from=")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--limit=")
    two_word_flags+=("--limit")
    local_nonpersistent_flags+=("--limit")
    local_nonpersistent_flags+=("--limit=")
    flags+=("--since=")
    two_word_flags+=("--since")
    local_nonpersistent_flags+=("--since")
    local_nonpersistent_flags+=("--since=")
    flags+=("--until=")
    two_word_flags+=("--until")
    local_nonpersistent_flags+=("--until")
    local_nonpersistent_flags+=("--until=")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_chat_show()
{
    last_command="teams_chat_show"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_chat()
{
    last_command="teams_chat"

    command_aliases=()

    commands=()
    commands+=("add-member")
    commands+=("create")
    commands+=("delete")
    commands+=("help")
    commands+=("list")
    commands+=("mark-read")
    commands+=("mark-unread")
    commands+=("read")
    commands+=("show")

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_config_get()
{
    last_command="teams_config_get"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_config_help()
{
    last_command="teams_config_help"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    has_completion_function=1
    noun_aliases=()
}

_teams_config_list()
{
    last_command="teams_config_list"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_config_set()
{
    last_command="teams_config_set"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_config()
{
    last_command="teams_config"

    command_aliases=()

    commands=()
    commands+=("get")
    commands+=("help")
    commands+=("list")
    commands+=("set")

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_delete()
{
    last_command="teams_delete"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--dry-run")
    local_nonpersistent_flags+=("--dry-run")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--team=")
    two_word_flags+=("--team")
    local_nonpersistent_flags+=("--team")
    local_nonpersistent_flags+=("--team=")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_doctor()
{
    last_command="teams_doctor"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--offline")
    local_nonpersistent_flags+=("--offline")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_edit()
{
    last_command="teams_edit"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--dry-run")
    local_nonpersistent_flags+=("--dry-run")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--html")
    local_nonpersistent_flags+=("--html")
    flags+=("--importance=")
    two_word_flags+=("--importance")
    local_nonpersistent_flags+=("--importance")
    local_nonpersistent_flags+=("--importance=")
    flags+=("--md")
    local_nonpersistent_flags+=("--md")
    flags+=("--mention=")
    two_word_flags+=("--mention")
    local_nonpersistent_flags+=("--mention")
    local_nonpersistent_flags+=("--mention=")
    flags+=("--subject=")
    two_word_flags+=("--subject")
    local_nonpersistent_flags+=("--subject")
    local_nonpersistent_flags+=("--subject=")
    flags+=("--team=")
    two_word_flags+=("--team")
    local_nonpersistent_flags+=("--team")
    local_nonpersistent_flags+=("--team=")
    flags+=("--text")
    local_nonpersistent_flags+=("--text")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_file_download()
{
    last_command="teams_file_download"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--name=")
    two_word_flags+=("--name")
    local_nonpersistent_flags+=("--name")
    local_nonpersistent_flags+=("--name=")
    flags+=("--no-images")
    local_nonpersistent_flags+=("--no-images")
    flags+=("--output=")
    two_word_flags+=("--output")
    two_word_flags+=("-o")
    local_nonpersistent_flags+=("--output")
    local_nonpersistent_flags+=("--output=")
    local_nonpersistent_flags+=("-o")
    flags+=("--overwrite")
    local_nonpersistent_flags+=("--overwrite")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_file_help()
{
    last_command="teams_file_help"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    has_completion_function=1
    noun_aliases=()
}

_teams_file()
{
    last_command="teams_file"

    command_aliases=()

    commands=()
    commands+=("download")
    commands+=("help")

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_help()
{
    last_command="teams_help"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    has_completion_function=1
    noun_aliases=()
}

_teams_mentions()
{
    last_command="teams_mentions"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--limit=")
    two_word_flags+=("--limit")
    local_nonpersistent_flags+=("--limit")
    local_nonpersistent_flags+=("--limit=")
    flags+=("--since=")
    two_word_flags+=("--since")
    local_nonpersistent_flags+=("--since")
    local_nonpersistent_flags+=("--since=")
    flags+=("--until=")
    two_word_flags+=("--until")
    local_nonpersistent_flags+=("--until")
    local_nonpersistent_flags+=("--until=")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_post()
{
    last_command="teams_post"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--dry-run")
    local_nonpersistent_flags+=("--dry-run")
    flags+=("--file=")
    two_word_flags+=("--file")
    local_nonpersistent_flags+=("--file")
    local_nonpersistent_flags+=("--file=")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--html")
    local_nonpersistent_flags+=("--html")
    flags+=("--importance=")
    two_word_flags+=("--importance")
    local_nonpersistent_flags+=("--importance")
    local_nonpersistent_flags+=("--importance=")
    flags+=("--md")
    local_nonpersistent_flags+=("--md")
    flags+=("--mention=")
    two_word_flags+=("--mention")
    local_nonpersistent_flags+=("--mention")
    local_nonpersistent_flags+=("--mention=")
    flags+=("--subject=")
    two_word_flags+=("--subject")
    local_nonpersistent_flags+=("--subject")
    local_nonpersistent_flags+=("--subject=")
    flags+=("--team=")
    two_word_flags+=("--team")
    local_nonpersistent_flags+=("--team")
    local_nonpersistent_flags+=("--team=")
    flags+=("--text")
    local_nonpersistent_flags+=("--text")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_profile_help()
{
    last_command="teams_profile_help"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    has_completion_function=1
    noun_aliases=()
}

_teams_profile_list()
{
    last_command="teams_profile_list"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_profile_use()
{
    last_command="teams_profile_use"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_profile()
{
    last_command="teams_profile"

    command_aliases=()

    commands=()
    commands+=("help")
    commands+=("list")
    commands+=("use")

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_react()
{
    last_command="teams_react"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--dry-run")
    local_nonpersistent_flags+=("--dry-run")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--remove")
    local_nonpersistent_flags+=("--remove")
    flags+=("--team=")
    two_word_flags+=("--team")
    local_nonpersistent_flags+=("--team")
    local_nonpersistent_flags+=("--team=")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_reply()
{
    last_command="teams_reply"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--dry-run")
    local_nonpersistent_flags+=("--dry-run")
    flags+=("--file=")
    two_word_flags+=("--file")
    local_nonpersistent_flags+=("--file")
    local_nonpersistent_flags+=("--file=")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--html")
    local_nonpersistent_flags+=("--html")
    flags+=("--importance=")
    two_word_flags+=("--importance")
    local_nonpersistent_flags+=("--importance")
    local_nonpersistent_flags+=("--importance=")
    flags+=("--md")
    local_nonpersistent_flags+=("--md")
    flags+=("--mention=")
    two_word_flags+=("--mention")
    local_nonpersistent_flags+=("--mention")
    local_nonpersistent_flags+=("--mention=")
    flags+=("--subject=")
    two_word_flags+=("--subject")
    local_nonpersistent_flags+=("--subject")
    local_nonpersistent_flags+=("--subject=")
    flags+=("--team=")
    two_word_flags+=("--team")
    local_nonpersistent_flags+=("--team")
    local_nonpersistent_flags+=("--team=")
    flags+=("--text")
    local_nonpersistent_flags+=("--text")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_search()
{
    last_command="teams_search"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--from=")
    two_word_flags+=("--from")
    local_nonpersistent_flags+=("--from")
    local_nonpersistent_flags+=("--from=")
    flags+=("--has-attachment")
    local_nonpersistent_flags+=("--has-attachment")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--in=")
    two_word_flags+=("--in")
    local_nonpersistent_flags+=("--in")
    local_nonpersistent_flags+=("--in=")
    flags+=("--limit=")
    two_word_flags+=("--limit")
    local_nonpersistent_flags+=("--limit")
    local_nonpersistent_flags+=("--limit=")
    flags+=("--mentions-me")
    local_nonpersistent_flags+=("--mentions-me")
    flags+=("--page=")
    two_word_flags+=("--page")
    local_nonpersistent_flags+=("--page")
    local_nonpersistent_flags+=("--page=")
    flags+=("--since=")
    two_word_flags+=("--since")
    local_nonpersistent_flags+=("--since")
    local_nonpersistent_flags+=("--since=")
    flags+=("--to=")
    two_word_flags+=("--to")
    local_nonpersistent_flags+=("--to")
    local_nonpersistent_flags+=("--to=")
    flags+=("--until=")
    two_word_flags+=("--until")
    local_nonpersistent_flags+=("--until")
    local_nonpersistent_flags+=("--until=")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_team_help()
{
    last_command="teams_team_help"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    has_completion_function=1
    noun_aliases=()
}

_teams_team_list()
{
    last_command="teams_team_list"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--all")
    local_nonpersistent_flags+=("--all")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--limit=")
    two_word_flags+=("--limit")
    local_nonpersistent_flags+=("--limit")
    local_nonpersistent_flags+=("--limit=")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_team_show()
{
    last_command="teams_team_show"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_team()
{
    last_command="teams_team"

    command_aliases=()

    commands=()
    commands+=("help")
    commands+=("list")
    commands+=("show")

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_thread_help()
{
    last_command="teams_thread_help"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    has_completion_function=1
    noun_aliases=()
}

_teams_thread_read()
{
    last_command="teams_thread_read"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--all")
    local_nonpersistent_flags+=("--all")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--limit=")
    two_word_flags+=("--limit")
    local_nonpersistent_flags+=("--limit")
    local_nonpersistent_flags+=("--limit=")
    flags+=("--since=")
    two_word_flags+=("--since")
    local_nonpersistent_flags+=("--since")
    local_nonpersistent_flags+=("--since=")
    flags+=("--team=")
    two_word_flags+=("--team")
    local_nonpersistent_flags+=("--team")
    local_nonpersistent_flags+=("--team=")
    flags+=("--until=")
    two_word_flags+=("--until")
    local_nonpersistent_flags+=("--until")
    local_nonpersistent_flags+=("--until=")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_thread()
{
    last_command="teams_thread"

    command_aliases=()

    commands=()
    commands+=("help")
    commands+=("read")

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_unread()
{
    last_command="teams_unread"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--all")
    local_nonpersistent_flags+=("--all")
    flags+=("--chats")
    local_nonpersistent_flags+=("--chats")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--mentions")
    local_nonpersistent_flags+=("--mentions")
    flags+=("--since=")
    two_word_flags+=("--since")
    local_nonpersistent_flags+=("--since")
    local_nonpersistent_flags+=("--since=")
    flags+=("--until=")
    two_word_flags+=("--until")
    local_nonpersistent_flags+=("--until")
    local_nonpersistent_flags+=("--until=")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_user_help()
{
    last_command="teams_user_help"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    has_completion_function=1
    noun_aliases=()
}

_teams_user_search()
{
    last_command="teams_user_search"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--all")
    local_nonpersistent_flags+=("--all")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--limit=")
    two_word_flags+=("--limit")
    local_nonpersistent_flags+=("--limit")
    local_nonpersistent_flags+=("--limit=")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_user_show()
{
    last_command="teams_user_show"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_user()
{
    last_command="teams_user"

    command_aliases=()

    commands=()
    commands+=("help")
    commands+=("search")
    commands+=("show")

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_version()
{
    last_command="teams_version"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--check")
    local_nonpersistent_flags+=("--check")
    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_whoami()
{
    last_command="teams_whoami"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_teams_root_command()
{
    last_command="teams"

    command_aliases=()

    commands=()
    commands+=("alias")
    commands+=("api")
    commands+=("auth")
    commands+=("cache")
    commands+=("channel")
    commands+=("chat")
    commands+=("config")
    commands+=("delete")
    commands+=("doctor")
    commands+=("edit")
    commands+=("file")
    commands+=("help")
    commands+=("mentions")
    commands+=("post")
    commands+=("profile")
    commands+=("react")
    commands+=("reply")
    commands+=("search")
    commands+=("team")
    commands+=("thread")
    commands+=("unread")
    commands+=("user")
    commands+=("version")
    commands+=("whoami")

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--help")
    flags+=("-h")
    local_nonpersistent_flags+=("--help")
    local_nonpersistent_flags+=("-h")
    flags+=("--jq=")
    two_word_flags+=("--jq")
    flags+=("--json")
    flags+=("--no-color")
    flags+=("--no-input")
    flags+=("--profile=")
    two_word_flags+=("--profile")
    flags+=("--quiet")
    flags+=("--read-only")
    flags+=("--refresh")
    flags+=("--verbose")
    flags+=("-v")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

__start_teams()
{
    local cur prev words cword split
    declare -A flaghash 2>/dev/null || :
    declare -A aliashash 2>/dev/null || :
    if declare -F _init_completion >/dev/null 2>&1; then
        _init_completion -s || return
    else
        __teams_init_completion -n "=" || return
    fi

    local c=0
    local flag_parsing_disabled=
    local flags=()
    local two_word_flags=()
    local local_nonpersistent_flags=()
    local flags_with_completion=()
    local flags_completion=()
    local commands=("teams")
    local command_aliases=()
    local must_have_one_flag=()
    local must_have_one_noun=()
    local has_completion_function=""
    local last_command=""
    local nouns=()
    local noun_aliases=()

    __teams_handle_word
}

if [[ $(type -t compopt) = "builtin" ]]; then
    complete -o default -F __start_teams teams
else
    complete -o default -o nospace -F __start_teams teams
fi

# ex: ts=4 sw=4 et filetype=sh
