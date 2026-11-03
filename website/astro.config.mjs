// @ts-check
import { defineConfig } from "astro/config";
import starlight from "@astrojs/starlight";

const section = (label, directory) => ({
  label,
  items: [{ autogenerate: { directory: `docs/${directory}` } }],
});

export default defineConfig({
  integrations: [
    starlight({
      title: "Commitarium",
      description:
        "Describe any app. Commitarium builds it, runs it, and changes it whenever you want.",
      social: [
        {
          icon: "github",
          label: "GitHub",
          href: "https://github.com/EinarLogiOskars/commitarium",
        },
      ],
      editLink: {
        baseUrl: "https://github.com/EinarLogiOskars/commitarium/edit/main/website/",
      },
      customCss: [
        "@fontsource-variable/geist",
        "@fontsource-variable/geist-mono",
        "./src/styles/theme.css",
      ],
      sidebar: [
        { label: "Overview", link: "/docs/" },
        section("Get started", "get-started"),
        section("Apps", "apps"),
        section("Developer mode", "developer-mode"),
        section("Your own code", "your-own-code"),
        section("Servers", "servers"),
        section("Putting apps online", "online"),
        section("Company sign-in", "company-sign-in"),
        section("Looking after a server", "server-care"),
        { slug: "docs/security" },
        { slug: "docs/troubleshooting" },
      ],
    }),
  ],
});
