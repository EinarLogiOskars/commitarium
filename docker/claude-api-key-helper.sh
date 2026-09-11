#!/bin/sh

# Claude Code supports API keys through an apiKeyHelper command. This helper
# keeps the key in the profile's private provider-state volume instead of a
# Compose environment variable. The `store` operation reads only from stdin;
# the secret is never a process argument.

set -eu

profile_dir="${CLAUDE_CONFIG_DIR:-/var/lib/commitarium-provider}"
key_file="${profile_dir}/commitarium-api-key"
settings_file="${profile_dir}/settings.json"
helper_path="/usr/local/bin/commitarium-claude-api-key"

case "${1:-}" in
    store)
        umask 077
        mkdir -p "${profile_dir}"
        key_tmp="${profile_dir}/.commitarium-api-key.$$"
        settings_tmp="${profile_dir}/.commitarium-settings.$$"
        trap 'rm -f "${key_tmp}" "${settings_tmp}"' EXIT INT TERM
        cat > "${key_tmp}"
        if [ ! -s "${key_tmp}" ]; then
            exit 2
        fi
        if [ -s "${settings_file}" ]; then
            jq --arg helper "${helper_path}" '.apiKeyHelper = $helper' \
                "${settings_file}" > "${settings_tmp}"
        else
            jq -n --arg helper "${helper_path}" '{apiKeyHelper: $helper}' \
                > "${settings_tmp}"
        fi
        chmod 0600 "${key_tmp}"
        chmod 0600 "${settings_tmp}"
        mv "${key_tmp}" "${key_file}"
        mv "${settings_tmp}" "${settings_file}"
        trap - EXIT INT TERM
        ;;
    print)
        exec cat "${key_file}"
        ;;
    clear)
        rm -f "${key_file}"
        if [ -s "${settings_file}" ]; then
            settings_tmp="${profile_dir}/.commitarium-settings.$$"
            trap 'rm -f "${settings_tmp}"' EXIT INT TERM
            jq --arg helper "${helper_path}" \
                'if .apiKeyHelper == $helper then del(.apiKeyHelper) else . end' \
                "${settings_file}" > "${settings_tmp}"
            chmod 0600 "${settings_tmp}"
            mv "${settings_tmp}" "${settings_file}"
            trap - EXIT INT TERM
        fi
        ;;
    *)
        exit 2
        ;;
esac
