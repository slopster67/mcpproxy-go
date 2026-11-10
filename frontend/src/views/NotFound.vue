<template>
  <div class="text-center py-20">
    <h1 class="text-6xl font-bold text-muted-foreground mb-4">404</h1>
    <h2 class="text-2xl font-semibold mb-4">Page Not Found</h2>
    <p class="text-muted-foreground mb-8">
      The page you're looking for doesn't exist or has been moved.
    </p>
    <router-link
      to="/servers"
      :class="cn(buttonVariants({ variant: 'default', size: 'default' }), '')"
    >
      Go to Servers
    </router-link>
  </div>
</template>

<script setup lang="ts">
import { cn } from "@/lib/utils";
import { buttonVariants } from "@/components/ui/button";
// This is a simple 404 page
</script>
