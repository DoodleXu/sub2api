<template>
  <div
    class="relative flex min-h-screen items-center justify-center overflow-hidden bg-gradient-to-br from-gray-50 via-primary-50/30 to-gray-100 px-4 py-12 dark:from-dark-950 dark:via-dark-900 dark:to-dark-950"
  >
    <div class="pointer-events-none absolute inset-0 overflow-hidden">
      <div
        class="absolute -right-40 -top-40 h-96 w-96 rounded-full bg-primary-400/20 blur-3xl"
      ></div>
      <div
        class="absolute -bottom-40 -left-40 h-96 w-96 rounded-full bg-primary-500/15 blur-3xl"
      ></div>
      <div
        class="absolute left-1/3 top-1/4 h-72 w-72 rounded-full bg-primary-300/10 blur-3xl"
      ></div>
      <div
        class="absolute inset-0 bg-[linear-gradient(rgba(20,184,166,0.03)_1px,transparent_1px),linear-gradient(90deg,rgba(20,184,166,0.03)_1px,transparent_1px)] bg-[size:64px_64px]"
      ></div>
    </div>

    <main class="relative z-10 w-full max-w-lg text-center">
      <div class="mb-8">
        <div class="inline-flex items-center justify-center gap-2 sm:gap-3">
          <span
            class="select-none text-[7rem] font-bold leading-none tracking-tight text-gray-100 dark:text-dark-800 sm:text-[9rem]"
          >
            4
          </span>
          <span
            class="select-none text-[7rem] font-bold leading-none tracking-tight text-gray-100 dark:text-dark-800 sm:text-[9rem]"
          >
            0
          </span>
          <span
            class="select-none text-[7rem] font-bold leading-none tracking-tight text-gray-100 dark:text-dark-800 sm:text-[9rem]"
          >
            3
          </span>
        </div>
      </div>

      <section class="space-y-4">
        <div class="space-y-3">
          <h1 class="text-2xl font-bold text-gray-900 dark:text-white sm:text-3xl">
            您所在的地区暂时无法访问此服务
          </h1>
          <p class="text-base leading-7 text-gray-600 dark:text-dark-300">
            This service is temporarily unavailable in your region.
          </p>
        </div>

        <p class="mx-auto max-w-md text-sm leading-6 text-gray-500 dark:text-dark-400">
          如果您认为这是误判，或需要了解可用区域信息，请联系支持团队。
          <br />
          If you believe this is a mistake, please contact support.
        </p>
      </section>

      <div class="mt-8 flex justify-center">
        <a
          href="https://t.me/luciferchatwith"
          target="_blank"
          rel="noopener noreferrer"
          class="btn btn-primary px-6"
        >
          联系支持 / Contact Support
        </a>
      </div>
    </main>
  </div>
</template>

<script setup lang="ts"></script>
