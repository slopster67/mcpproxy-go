import Usage from "@/views/Usage.vue";
import Clients from "@/views/Clients.vue";
import Profiles from "@/views/Profiles.vue";
import ProfileEditor from "@/views/ProfileEditor.vue";
import Servers from "@/views/Servers.vue";
import ServerDetail from "@/views/ServerDetail.vue";
import Secrets from "@/views/Secrets.vue";
import Tools from "@/views/Tools.vue";
import Activity from "@/views/Activity.vue";
import NotFound from "@/views/NotFound.vue";
import { createRouter, createWebHistory } from "vue-router";

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    {
      path: "/",
      redirect: (to) => ({ path: "/servers", query: to.query, hash: to.hash }),
    },
    {
      path: "/usage",
      name: "usage",
      component: Usage,
      meta: {
        title: "Usage Analytics",
      },
    },
    {
      path: "/clients",
      name: "clients",
      component: Clients,
      meta: { title: "Clients" },
    },
    {
      path: "/profiles",
      name: "profiles",
      component: Profiles,
      meta: { title: "Profiles" },
    },
    {
      path: "/profiles/:id",
      name: "profile-editor",
      component: ProfileEditor,
      props: true,
      meta: { title: "Profile" },
    },
    {
      path: "/servers",
      name: "servers",
      component: Servers,
      meta: {
        title: "Servers",
      },
    },
    {
      path: "/servers/:serverName",
      name: "server-detail",
      component: ServerDetail,
      props: true,
      meta: {
        title: "Server Details",
      },
    },
    {
      path: "/secrets",
      name: "secrets",
      component: Secrets,
      meta: {
        title: "Secrets",
      },
    },
    {
      path: "/tools",
      name: "tools",
      component: Tools,
      meta: {
        title: "Tools",
      },
    },
    {
      path: "/activity",
      name: "activity",
      component: Activity,
      meta: {
        title: "Activity Log",
      },
    },

    {
      path: "/:pathMatch(.*)*",
      name: "not-found",
      component: NotFound,
      meta: {
        title: "Page Not Found",
      },
    },
  ],
});

router.beforeEach((to) => {
  const title = to.meta.title as string;
  if (title) {
    document.title = `${title} - MCPProxy Control Panel`;
  }
});

export default router;
