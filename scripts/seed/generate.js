#!/usr/bin/env node
// Deterministic dataset generator for DISBank (IMPROVEMENT_PLAN §0.5).
//
// Replaces generate_mock_users.js / generate_mock_transactions.js + the manual
// Compass import + the uncommitted password-hashing step. This script:
//   - seeds faker so the same --seed always produces the same dataset
//   - hashes passwords with bcrypt at generation time (handlers/login.go
//     verifies with bcrypt.CompareHashAndPassword, so this is the one source
//     of truth for credentials)
//   - writes users.json / transactions.json in the shape mongo-init's
//     mongoimport expects, plus a credentials.json side-file (plaintext,
//     NOT imported into Mongo) for regenerating test fixtures
//
// Usage:
//   node scripts/seed/generate.js [--users N] [--transactions M] [--seed S] [--out DIR] [--bcrypt-cost C]
//
// Defaults produce the ~5k-user / ~50k-txn dataset described in the plan.
// bcrypt at cost 10 (the default; handlers/login.go reads cost from the hash
// itself, so any valid cost works) takes a few minutes for 5k users — pass a
// lower --bcrypt-cost for a faster local run if you don't need that.
// `npm run seed:sample` calls this with small numbers and the same cost to
// produce the tiny, committed dataset under scripts/seed/sample/ that
// CI/reviewers load without running this generator themselves.

const fs = require("fs");
const path = require("path");
const { faker } = require("@faker-js/faker");
const bcrypt = require("bcryptjs");

function parseArgs(argv) {
  const defaults = {
    users: 5000,
    transactions: 50000,
    seed: 512,
    out: "scripts/seed/data",
    "bcrypt-cost": 10,
  };
  const args = { ...defaults };
  for (let i = 0; i < argv.length; i++) {
    const key = argv[i].replace(/^--/, "");
    if (key in defaults) {
      args[key] = argv[++i];
    }
  }
  args.users = parseInt(args.users, 10);
  args.transactions = parseInt(args.transactions, 10);
  args.seed = parseInt(args.seed, 10);
  args.bcryptCost = parseInt(args["bcrypt-cost"], 10);
  return args;
}

function generateUsers(numUsers, bcryptCost) {
  const users = [];
  const credentials = [];
  const usedAccountNumbers = new Set();

  for (let i = 0; i < numUsers; i++) {
    let accountNumber;
    do {
      accountNumber = faker.number.int({ min: 100000000, max: 999999999 });
    } while (usedAccountNumbers.has(accountNumber));
    usedAccountNumbers.add(accountNumber);

    const userId = 100 + i;
    const firstName = faker.person.firstName();
    const lastName = faker.person.lastName();
    const email = faker.internet.email({ firstName, lastName });
    const plaintextPassword = faker.internet.password({ length: 12, pattern: /[A-Za-z0-9]/ });
    // current_balance is integer cents (§1.2): $10,000.00-$999,999.00 -> 1000000-99999900 cents
    // (same dollar range as before the cents migration, multiplied by 100).
    const currentBalance = faker.number.int({ min: 1000000, max: 99999900 });

    users.push({
      user_id: userId,
      first_name: firstName,
      last_name: lastName,
      email,
      current_balance: currentBalance,
      password: bcrypt.hashSync(plaintextPassword, bcryptCost),
      account_number: accountNumber,
    });

    credentials.push({ user_id: userId, email, password: plaintextPassword });
  }

  return { users, credentials };
}

function generateTransactions(users, numTransactions) {
  const userIds = users.map((u) => u.user_id);
  const userMap = Object.fromEntries(users.map((u) => [u.user_id, `${u.first_name} ${u.last_name}`]));
  const transactionTypes = ["transfer", "deposit", "withdrawal"];
  const startDate = new Date(2022, 0, 1);
  const endDate = new Date(2026, 0, 1); // fixed, not Date.now(), to stay deterministic

  function randomDate(start, end) {
    return new Date(start.getTime() + faker.number.float({ min: 0, max: 1 }) * (end.getTime() - start.getTime()));
  }

  const transactions = [];
  for (let txnId = 1; txnId <= numTransactions; txnId++) {
    const txnType = faker.helpers.arrayElement(transactionTypes);
    const senderId = faker.helpers.arrayElement(userIds);
    let receiverId = txnType === "transfer" ? faker.helpers.arrayElement(userIds) : senderId;
    // amount is integer cents (§1.2), matching datamodels.Transaction.Amount;
    // handlers/monthdata.go's CSV export divides by 100 before `$%.2f`.
    // $500.00-$15,000.00 -> 50000-1500000 cents (same dollar range as before
    // the cents migration, multiplied by 100).
    let amount = faker.number.int({ min: 50000, max: 1500000 });
    const timestamp = Math.floor(randomDate(startDate, endDate).getTime() / 1000);
    let status = "completed";
    if (txnType === "transfer" && faker.number.float({ min: 0, max: 1 }) < 0.02) {
      status = "failed";
    }

    const senderName = userMap[senderId];
    const receiverName = userMap[receiverId];
    let remarks;
    if (txnType === "deposit") {
      remarks = `Deposit of $${(amount / 100).toFixed(2)} by ${senderName}`;
      receiverId = senderId;
    } else if (txnType === "withdrawal") {
      remarks = `Withdrawal of $${(amount / 100).toFixed(2)} by ${senderName}`;
      receiverId = senderId;
      amount = -amount;
    } else {
      remarks = `Transfer of $${(amount / 100).toFixed(2)} from ${senderName} to ${receiverName}`;
    }

    transactions.push({
      transaction_id: txnId,
      sender_id: senderId,
      amount,
      receiver_id: receiverId,
      remarks,
      dateTimeStamp: timestamp,
      status,
    });
  }
  return transactions;
}

function writeJson(filePath, data) {
  fs.mkdirSync(path.dirname(filePath), { recursive: true });
  fs.writeFileSync(filePath, JSON.stringify(data, null, 2), "utf-8");
  console.log(`wrote ${data.length} records to ${filePath}`);
}

function main() {
  const args = parseArgs(process.argv.slice(2));
  faker.seed(args.seed);

  console.log(
    `generating ${args.users} users / ${args.transactions} transactions ` +
      `(seed=${args.seed}, bcrypt cost=${args.bcryptCost})...`
  );
  const { users, credentials } = generateUsers(args.users, args.bcryptCost);
  const transactions = generateTransactions(users, args.transactions);

  writeJson(path.join(args.out, "users.json"), users);
  writeJson(path.join(args.out, "transactions.json"), transactions);
  writeJson(path.join(args.out, "credentials.json"), credentials);

  console.log(
    "done. users.json/transactions.json are what mongoimport loads; " +
      "credentials.json (plaintext passwords, not imported) is only for regenerating test fixtures."
  );
}

main();
